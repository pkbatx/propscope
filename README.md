# propscope

HF propagation at a glance, in your terminal, from measurements rather than
predictions.

```
▌ BANDS NOW  03:40Z / 22:40 CDT ──────────────  ▌ SOLAR  12m old ─────────────
  160m ███▏                    784  NVIS+DX      SFI   136.0 ████████──── 24h 134
   80m ███████████████████▋   4991  NVIS+DX      SSN    98.1 █████████─── 24h 95
   40m ██████████████████████ 11737 NVIS+DX      Kp     4.33 █████████─── active
   20m ███████████████████████ 11972 DX ONLY     absrb   0.0 ──────────── MHz LUF
   10m █▍                      343  CLOSED
▌ 24H WATERFALL ──────────────────────────────────────────────────────────────
    6m ▓▓██████▓██▓█▓█▓███▓█▓▓████▓▓██▓██▓▓▓▓▓▓▓▓▒▓▓▓▓▓▓▓▓▓▓▓▒▓▓▓▓▓▒▓▓▓▓▓▓▓▓
   20m ███████████████████████▓█▓▓▓▓▓▓▓▓▓▓▓▓▓▓█████████████████▓██▓█████████
  160m █▓▓▓▓▓▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒░▒▒░░▒░░░░░░░░░░░░░░░░░░░░░░▒▒▒▒▓▒▓▓▓██▓
```

## What it shows

- **Which bands are actually open**, from every WSPR reception report worldwide
  in 10-minute buckets. This is measured propagation, not a prediction.
- **A 24-hour waterfall** of band activity, so the diurnal pattern is visible at
  a glance: low bands opening after dark, high bands at midday.
- **Solar drivers** — 10.7 cm flux, sunspot number, planetary Kp, and the
  *effective* SSN/SFI fitted from real ionosonde soundings.
- **A band-by-band open/closed estimate** derived from foF2 and MUF(3000) at the
  nearest reporting ionosonde, with D-region absorption setting the lower edge.

## Quick start

Requires Docker with the Compose plugin. Nothing else.

```bash
git clone <this repo> propscope && cd propscope
cp .env.example .env          # set PGPASSWORD, and your QTH if not Austin
docker compose up -d          # postgres + collector
docker compose run --rm tui   # the dashboard
```

The collector backfills 24 hours of WSPR history on its first run, so the
waterfall is populated within a minute or two rather than tomorrow. Solar and
ionosonde history backfill too — roughly 6 days of effective SSN and 30 days of
daily flux arrive immediately.

Check it is collecting:

```bash
docker compose logs -f collector
docker compose exec collector python -c \
  "import urllib.request;print(urllib.request.urlopen('http://localhost:8000/api/sources').read().decode())"
```

### Running the TUI outside Docker

The TUI is a single static binary and only needs to reach postgres. It follows
the usual libpq environment variables — `PGHOST`, `PGPORT`, `PGDATABASE`,
`PGUSER`, `PGPASSWORD` — and if `PGPASSWORD` is unset it falls back to
`PGPASSFILE` / `~/.pgpass` like any other postgres client:

```bash
go build -o propscope .
PGHOST=127.0.0.1 PGPORT=5432 PGPASSWORD=... ./propscope
```

`propscope -dump` renders every tab once to stdout and exits — useful for a
cron mail, `less -R`, or checking it from a script. `-plain` drops the colour.

## Keys

| key | |
|---|---|
| `1`–`5` / `tab` | HOME, BANDS, WATERFALL, SOLAR, IONOSPHERE |
| `r` | refresh now (it also polls every 30 s) |
| `?` | what the numbers mean |
| `q` | quit |

## Configuration

Everything is environment variables; see `.env.example`. The two that matter:

| variable | default | |
|---|---|---|
| `QTH_LAT` / `QTH_LON` | Austin, TX | your station, decimal degrees, west negative |
| `PROPSCOPE_TZ` | `America/Chicago` | wall clock shown beside Zulu |

Set `NTFY_ALERT_TOPIC` (and `NTFY_URL`) for a push when Kp reaches 5 or a data
source fails repeatedly. Leave it unset and no notification is ever attempted.

## Where the data comes from

| what | source | interval |
|---|---|---|
| spots per band | [wspr.live](https://wspr.live) ClickHouse mirror of WSPRnet | 10 min |
| 10.7 cm flux, planetary Kp | NOAA SWPC JSON products | 15–60 min |
| daily sunspot number | NOAA SWPC daily solar data report (30-day history) | 60 min |
| effective SSN / SFI | [prop.kc2g.com](https://prop.kc2g.com) | 15 min |
| foF2 / MUF soundings | GIRO ionosondes via prop.kc2g.com | 15 min |
| D-region absorption | NOAA D-RAP global grid, sampled at your QTH | 15 min |

All are free public services run by volunteers and government agencies. **The
intervals above are deliberate — do not shorten them.** One datacenter IP
hammering wspr.live gets the range banned for everyone.

## How the band status is worked out

For each band, comparing its frequency against the reference station's sounding:

| | |
|---|---|
| **NVIS+DX** | at or below foF2 — reflects at vertical incidence, so local *and* long paths work |
| **DX ONLY** | above foF2, at or below MUF(3000) — skip paths only, with a dead zone around you |
| **MARGINAL** | within 15 % above MUF; MUF is a median, so this opens on a good day |
| **SPORADIC-E** | F2 cannot carry it, but Es can (roughly 5 × foEs obliquely) |
| **ABSORBED** | below the D-region absorption limit |
| **CLOSED** | above MUF with no Es |

These are rules of thumb from **one** station's vertical sounding. It is a
decision aid, not a forecast — and the reference station is very likely not
near you.

## A note on ionosondes

The reference station is whichever *currently reporting* ionosonde is nearest to
your QTH, and it may be a long way off. Of the ~100 stations GIRO publishes,
only around 30 report on any given day, and their freshness is bimodal: a
couple update continuously while most arrive in a batch around 20:50 UTC. The
IONOSPHERE tab always shows the age of the sounding it used.

If you are near Austin: **AU930 "Austin, TX, USA" is dead.** It sits 15 km from
the default QTH and would be ideal, but its last sounding was 2026-03-19, and
GIRO's own `DIDBGetValues` servlet now returns 404 at every parameter format, so
there is no second route to it. The nearest live stations are Idaho National
Lab (~2000 km) and Millstone Hill, MA (~2700 km). propscope shows measured data
only — it will not model a foF2 for you and present it as an observation.

## Licence

MIT.
