package main

import "fmt"

// Band is one amateur HF/VHF allocation. MHz is the frequency the propagation
// arithmetic uses -- the bottom of the band, near enough, since we are
// comparing against a MUF whose own uncertainty is far wider than a band's
// width. WSPR is the integer bucket wspr.live reports for this band; it is the
// band edge in whole MHz, not a channel.
type Band struct {
	Name string
	MHz  float64
	WSPR int
}

var Bands = []Band{
	{"160m", 1.8, 1},
	{"80m", 3.5, 3},
	{"60m", 5.36, 5},
	{"40m", 7.0, 7},
	{"30m", 10.1, 10},
	{"20m", 14.0, 14},
	{"17m", 18.1, 18},
	{"15m", 21.0, 21},
	{"12m", 24.9, 24},
	{"10m", 28.0, 28},
	{"6m", 50.0, 50},
}

// wsprBandName maps a wspr.live band bucket to a human label. The feed carries
// buckets we do not have a ham band for (-1 is explicitly "out of band") so
// unknown codes fall through to the raw MHz rather than being dropped -- a
// mystery bucket with 3000 spots in it is worth seeing.
func wsprBandName(code int) string {
	for _, b := range Bands {
		if b.WSPR == code {
			return b.Name
		}
	}
	switch code {
	case -1:
		return "oob"
	case 0:
		return "LF/MF"
	case 70:
		return "4m"
	case 144:
		return "2m"
	case 432:
		return "70cm"
	case 1296:
		return "23cm"
	}
	return fmt.Sprintf("%dMHz", code)
}

// Status is the modelled state of a band for the reference station.
type Status int

const (
	StatusUnknown Status = iota
	StatusAbsorbed
	StatusNVIS
	StatusDX
	StatusMarginal
	StatusES
	StatusClosed
)

func (s Status) Label() string {
	switch s {
	case StatusAbsorbed:
		return "ABSORBED"
	case StatusNVIS:
		return "NVIS+DX"
	case StatusDX:
		return "DX ONLY"
	case StatusMarginal:
		return "MARGINAL"
	case StatusES:
		return "SPORADIC-E"
	case StatusClosed:
		return "CLOSED"
	}
	return "NO DATA"
}

// Why explains the classification in the terms that produced it, so a number on
// screen can always be traced back to the measurement behind it.
func (s Status) Why() string {
	switch s {
	case StatusAbsorbed:
		return "below the D-region absorption limit"
	case StatusNVIS:
		return "at or below foF2 - reflects at vertical incidence, so local and DX both work"
	case StatusDX:
		return "above foF2 but at or below MUF(3000) - skip paths only, no local coverage"
	case StatusMarginal:
		return "within 15% above MUF(3000) - MUF is a median, so this opens on a good day"
	case StatusES:
		return "above the F2 MUF but supported by sporadic E"
	case StatusClosed:
		return "above MUF(3000) with no sporadic E to carry it"
	}
	return "no usable sounding"
}

// Classify estimates whether a band is usable, from one station's sounding.
//
// The model, stated plainly so it can be argued with:
//
//   - foF2 is the F2 critical frequency at vertical incidence. At or below it,
//     signals come straight back down: NVIS, local coverage, and DX too.
//   - MUF(3000) is the maximum usable frequency for a single 3000 km hop. It is
//     the practical ceiling for ordinary DX. Between foF2 and MUF the band is
//     skip-only, and the minimum workable distance grows as you approach MUF.
//   - MUF is a MEDIAN, not a wall: roughly half of days beat it. The 15%
//     marginal band above it is the usual allowance for that spread.
//   - foEs is the sporadic-E critical frequency. Es supports oblique paths up
//     to roughly 5x foEs, which is how 10m and 6m open with a dead F2 layer.
//   - haf is D-RAP's Highest Affected Frequency at the QTH. Below it the
//     D-region eats the signal. It is zero at night, when there is no D region.
//
// Every one of those is a rule of thumb. This is a decision aid, not a forecast.
func Classify(b Band, s *Sounding, haf float64) Status {
	if s == nil || s.FoF2 <= 0 || s.MUFD <= 0 {
		return StatusUnknown
	}
	if haf > 0 && b.MHz < haf {
		return StatusAbsorbed
	}
	switch {
	case b.MHz <= s.FoF2:
		return StatusNVIS
	case b.MHz <= s.MUFD:
		return StatusDX
	case b.MHz <= s.MUFD*1.15:
		return StatusMarginal
	}
	// F2 cannot carry it; sporadic E still might.
	if s.FoEs > 0 && b.MHz <= s.FoEs*5.0 {
		return StatusES
	}
	return StatusClosed
}
