package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Snapshot is everything one refresh pulls, so the UI only ever renders a
// self-consistent view rather than a mix of ages.
type Snapshot struct {
	Spots       []BandSpots
	SpotBucket  time.Time
	Solar       Solar
	ESSN        []ESSNPoint
	Daily       []DailyPoint
	Soundings   []Sounding
	AustinAge   time.Duration // how stale the local AU930 sounder is
	AustinKnown bool
	Err         error
	At          time.Time

	// Hist is spots-per-band over the last 24h, one slice per WSPR band code,
	// oldest bucket first and gap-filled with zeros so every band's slice is
	// the same length as HistTimes. That alignment is what lets the waterfall
	// be pushed as a matrix and the per-band sparklines share an x axis.
	Hist      map[int][]float64
	HistTimes []time.Time
}

type BandSpots struct {
	Band    int
	Spots   int64
	TxCount int64
	RxCount int64
	AvgSNR  float64
	MaxKM   float64
}

type Solar struct {
	SSN6h, SFI6h   float64
	SSN24h, SFI24h float64
	ESSNTime       time.Time

	Day       time.Time
	DailyFlux float64
	DailySSN  int
	XrayC     int
	XrayM     int
	XrayX     int

	Kp     float64
	KpTime time.Time

	HAF       float64
	DrapValid time.Time
	HasDrap   bool
}

type ESSNPoint struct {
	T        time.Time
	SSN, SFI float64
	Span     string
}

type DailyPoint struct {
	D    time.Time
	Flux float64
	SSN  int
}

type Sounding struct {
	Code string
	Name string
	KM   float64
	FoF2 float64
	MUFD float64
	HmF2 float64
	FoEs float64
	CS   float64
	T    time.Time
}

func (s Sounding) Age() time.Duration { return time.Since(s.T) }

// Connect opens the pool.
//
// Connection settings follow the standard libpq environment variables, which
// pgx implements: PGHOST, PGPORT, PGDATABASE, PGUSER, PGPASSWORD. The password
// is deliberately NOT read by this program -- if PGPASSWORD is unset the DSN
// simply omits it and pgx falls back to PGPASSFILE or ~/.pgpass, which is the
// convention every other postgres client already follows and the one users
// already have tooling for. Reinventing that would mean inventing a new place
// for people to leave a password lying around.
func Connect(ctx context.Context) (*pgxpool.Pool, error) {
	parts := []string{
		"host=" + getenv("PGHOST", "127.0.0.1"),
		"port=" + getenv("PGPORT", "5432"),
		"dbname=" + getenv("PGDATABASE", "propscope"),
		"user=" + getenv("PGUSER", "propscope"),
		"application_name=propscope",
		"connect_timeout=10",
	}
	if pw := os.Getenv("PGPASSWORD"); pw != "" {
		parts = append(parts, "password="+pw)
	}

	cfg, err := pgxpool.ParseConfig(strings.Join(parts, " "))
	if err != nil {
		return nil, err
	}
	// One core, one reader. There is no concurrency to serve here.
	cfg.MaxConns = 2
	cfg.MinConns = 1
	return pgxpool.NewWithConfig(ctx, cfg)
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Load pulls one complete snapshot. Any single query failing aborts the
// snapshot: a half-populated dashboard is worse than an error line, because it
// looks like a band went quiet.
func Load(ctx context.Context, pool *pgxpool.Pool) Snapshot {
	s := Snapshot{At: time.Now()}

	// --- spots per band, most recent CLOSED 10-minute bucket ---------------
	// The newest bucket is still filling, so its counts are partial and would
	// read as a band collapsing. Skip it.
	const spotsQ = `
WITH b AS (
  SELECT max(bucket) AS bucket FROM wspr_band_activity
  WHERE bucket <= now() - interval '10 minutes'
)
SELECT w.bucket, w.band, w.spots,
       coalesce(w.tx_count,0), coalesce(w.rx_count,0),
       coalesce(w.avg_snr,0), coalesce(w.max_km,0)
FROM wspr_band_activity w JOIN b ON w.bucket = b.bucket
ORDER BY w.spots DESC`
	rows, err := pool.Query(ctx, spotsQ)
	if err != nil {
		s.Err = fmt.Errorf("spots: %w", err)
		return s
	}
	for rows.Next() {
		var x BandSpots
		var bucket time.Time
		if err := rows.Scan(&bucket, &x.Band, &x.Spots, &x.TxCount, &x.RxCount,
			&x.AvgSNR, &x.MaxKM); err != nil {
			rows.Close()
			s.Err = fmt.Errorf("spots scan: %w", err)
			return s
		}
		s.SpotBucket = bucket
		s.Spots = append(s.Spots, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		s.Err = fmt.Errorf("spots: %w", err)
		return s
	}

	// --- 24h of spots per band, for the waterfall and the trend sparklines --
	//
	// Gap-filling matters here: wspr.live returns no row at all for a band with
	// zero spots in a bucket, and a missing bucket is not the same shape as a
	// dead band. Building the time axis first and indexing into it keeps every
	// band's slice aligned even when the collector was down.
	const histQ = `
SELECT bucket, band, spots FROM wspr_band_activity
WHERE bucket > now() - interval '24 hours'
ORDER BY bucket`
	rows, err = pool.Query(ctx, histQ)
	if err != nil {
		s.Err = fmt.Errorf("history: %w", err)
		return s
	}
	type hp struct {
		t     time.Time
		band  int
		spots int64
	}
	var hps []hp
	seen := map[time.Time]bool{}
	for rows.Next() {
		var p hp
		if err := rows.Scan(&p.t, &p.band, &p.spots); err != nil {
			rows.Close()
			s.Err = fmt.Errorf("history scan: %w", err)
			return s
		}
		hps = append(hps, p)
		if !seen[p.t] {
			seen[p.t] = true
			s.HistTimes = append(s.HistTimes, p.t)
		}
	}
	rows.Close()
	idx := make(map[time.Time]int, len(s.HistTimes))
	for i, t := range s.HistTimes {
		idx[t] = i
	}
	s.Hist = map[int][]float64{}
	for _, p := range hps {
		if _, ok := s.Hist[p.band]; !ok {
			s.Hist[p.band] = make([]float64, len(s.HistTimes))
		}
		s.Hist[p.band][idx[p.t]] = float64(p.spots)
	}

	// --- effective SSN/SFI history, both smoothing spans -------------------
	const essnQ = `
SELECT time, span, coalesce(ssn,0), coalesce(sfi,0)
FROM essn WHERE time > now() - interval '7 days'
ORDER BY time`
	rows, err = pool.Query(ctx, essnQ)
	if err != nil {
		s.Err = fmt.Errorf("essn: %w", err)
		return s
	}
	for rows.Next() {
		var p ESSNPoint
		if err := rows.Scan(&p.T, &p.Span, &p.SSN, &p.SFI); err != nil {
			rows.Close()
			s.Err = fmt.Errorf("essn scan: %w", err)
			return s
		}
		s.ESSN = append(s.ESSN, p)
	}
	rows.Close()
	for _, p := range s.ESSN {
		if p.T.After(s.Solar.ESSNTime) {
			s.Solar.ESSNTime = p.T
		}
	}
	for _, p := range s.ESSN {
		if !p.T.Equal(s.Solar.ESSNTime) {
			continue
		}
		if p.Span == "6h" {
			s.Solar.SSN6h, s.Solar.SFI6h = p.SSN, p.SFI
		} else {
			s.Solar.SSN24h, s.Solar.SFI24h = p.SSN, p.SFI
		}
	}

	// --- daily sunspot number + flux, 30 days ------------------------------
	const dailyQ = `
SELECT day, coalesce(flux_10cm,0), coalesce(sunspot_number,0)
FROM solar_daily ORDER BY day`
	rows, err = pool.Query(ctx, dailyQ)
	if err != nil {
		s.Err = fmt.Errorf("solar_daily: %w", err)
		return s
	}
	for rows.Next() {
		var p DailyPoint
		if err := rows.Scan(&p.D, &p.Flux, &p.SSN); err != nil {
			rows.Close()
			s.Err = fmt.Errorf("solar_daily scan: %w", err)
			return s
		}
		s.Daily = append(s.Daily, p)
	}
	rows.Close()
	if n := len(s.Daily); n > 0 {
		last := s.Daily[n-1]
		s.Solar.Day, s.Solar.DailyFlux, s.Solar.DailySSN = last.D, last.Flux, last.SSN
	}
	_ = pool.QueryRow(ctx,
		`SELECT coalesce(xray_c,0), coalesce(xray_m,0), coalesce(xray_x,0)
		 FROM solar_daily ORDER BY day DESC LIMIT 1`).
		Scan(&s.Solar.XrayC, &s.Solar.XrayM, &s.Solar.XrayX)

	// --- latest Kp ---------------------------------------------------------
	_ = pool.QueryRow(ctx,
		`SELECT time_tag, kp FROM swpc_kp ORDER BY time_tag DESC LIMIT 1`).
		Scan(&s.Solar.KpTime, &s.Solar.Kp)

	// --- D-RAP absorption at the QTH ---------------------------------------
	if err := pool.QueryRow(ctx,
		`SELECT valid_at, coalesce(haf_mhz,0) FROM drap_local
		 ORDER BY valid_at DESC LIMIT 1`).
		Scan(&s.Solar.DrapValid, &s.Solar.HAF); err == nil {
		s.Solar.HasDrap = true
	}

	// --- soundings: latest per station, nearest first -----------------------
	// LATERAL rather than DISTINCT ON so the per-station index is actually
	// used; there are only ~100 stations but this runs every 30s.
	const sndQ = `
SELECT st.code, st.name, coalesce(st.km_from_qth,0),
       coalesce(o.fof2,0), coalesce(o.mufd,0), coalesce(o.hmf2,0),
       coalesce(o.foes,0), coalesce(o.cs,-1), o.time
FROM ionosonde_station st
JOIN LATERAL (
  SELECT * FROM ionosonde_obs o
  WHERE o.station = st.code ORDER BY o.time DESC LIMIT 1
) o ON true
WHERE o.fof2 IS NOT NULL AND o.mufd IS NOT NULL
ORDER BY st.km_from_qth
LIMIT 40`
	rows, err = pool.Query(ctx, sndQ)
	if err != nil {
		s.Err = fmt.Errorf("soundings: %w", err)
		return s
	}
	for rows.Next() {
		var x Sounding
		if err := rows.Scan(&x.Code, &x.Name, &x.KM, &x.FoF2, &x.MUFD,
			&x.HmF2, &x.FoEs, &x.CS, &x.T); err != nil {
			rows.Close()
			s.Err = fmt.Errorf("soundings scan: %w", err)
			return s
		}
		s.Soundings = append(s.Soundings, x)
	}
	rows.Close()

	// --- how dead is the local sounder? -------------------------------------
	// AU930 sits 15 km from the QTH and would be the ideal reference. It has
	// been silent since 2026-03-19. Surfacing its age is the honest way to
	// explain why the reference station is 2000 km away.
	var auLast time.Time
	if err := pool.QueryRow(ctx,
		`SELECT last_seen FROM ionosonde_station WHERE code = 'AU930'`).
		Scan(&auLast); err == nil && !auLast.IsZero() {
		s.AustinKnown = true
		s.AustinAge = time.Since(auLast)
	}

	return s
}

// Reference picks the station the band calculation speaks for: the nearest one
// whose sounding is both recent and autoscaled with some confidence.
//
// cs is the autoscaling confidence score. -1 means "not scored" and 0 means the
// scaler had no confidence at all; either way the numbers are not worth
// building a prediction on when a better station is available. If nothing
// clears the bar we fall back to the nearest sounding of any quality rather
// than showing nothing, and the UI says so.
func (s Snapshot) Reference() (*Sounding, bool) {
	const maxAge = 24 * time.Hour
	for i := range s.Soundings {
		x := &s.Soundings[i]
		if x.Age() <= maxAge && x.CS >= 25 {
			return x, true
		}
	}
	for i := range s.Soundings {
		x := &s.Soundings[i]
		if x.Age() <= maxAge {
			return x, false
		}
	}
	return nil, false
}
