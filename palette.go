package main

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Two kinds of colour live here, and the distinction matters.
//
// IDENTITY colours -- the per-band spectrum, status badges, Kp bands -- stay
// indexed ANSI-256. They are names, not points on a scale, and an index renders
// identically everywhere.
//
// GRADIENT colours -- the waterfall, gauges, scaled numbers -- are defined in
// 24-bit and handed to lipgloss, which quantises to whatever profile is active
// (see wantProfile in main.go). On a truecolor terminal the ramp is continuous;
// on a 256-colour one it lands on the same cube an indexed ramp would have.

// bandColor maps a band to its place on a warm-to-cool spectrum: 160m red
// through 6m violet. It is decorative, but it is also a real mnemonic -- the
// eye learns "low bands are warm" and can then read the waterfall without
// consulting a legend.
func bandColor(wsprCode int) lipgloss.Color {
	switch wsprCode {
	case 1: // 160m
		return lipgloss.Color("196")
	case 3: // 80m
		return lipgloss.Color("202")
	case 5: // 60m
		return lipgloss.Color("208")
	case 7: // 40m
		return lipgloss.Color("214")
	case 10: // 30m
		return lipgloss.Color("220")
	case 14: // 20m
		return lipgloss.Color("190")
	case 18: // 17m
		return lipgloss.Color("118")
	case 21: // 15m
		return lipgloss.Color("48")
	case 24: // 12m
		return lipgloss.Color("51")
	case 28: // 10m
		return lipgloss.Color("39")
	case 50: // 6m
		return lipgloss.Color("141")
	}
	return lipgloss.Color("245")
}

// ---------------------------------------------------------------------------
// Smooth colour ramps.
//
// These return 24-bit hex and let lipgloss quantise. That is the opposite of
// what this file used to do, and the reason is that lipgloss downsamples hex to
// the ACTIVE profile: on a truecolor terminal a gradient is genuinely smooth,
// and on a 256-colour one it lands on the same cube it would have anyway. One
// definition, best available output. The fixed indexed ramps below are kept for
// the handful of places that want a specific named colour rather than a point
// on a gradient.

func hex(r, g, b float64) lipgloss.Color {
	c := func(v float64) int {
		i := int(v*255 + 0.5)
		if i < 0 {
			return 0
		}
		if i > 255 {
			return 255
		}
		return i
	}
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", c(r), c(g), c(b)))
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// heatAt is the waterfall colormap: near-black through blue, cyan, green and
// yellow to red — the shape every receiver waterfall has used for decades.
// Piecewise-linear in RGB, which is crude colour science but reads correctly
// and, unlike a perceptual space, never muddies the mid-tones on a dark
// terminal background.
func heatAt(t float64) lipgloss.Color {
	t = clamp01(t)
	r := clamp01(1.5 - math.Abs(4*t-3))
	g := clamp01(1.5 - math.Abs(4*t-2))
	b := clamp01(1.5 - math.Abs(4*t-1))
	// Fade the coldest end to near-black so "quiet" reads as absence.
	if t < 0.12 {
		f := t / 0.12
		r, g, b = r*f, g*f, b*f
	}
	return hex(r, g, b)
}

// warmAt is for scalar readouts where higher simply means hotter: blue through
// cyan and green to yellow, orange, red.
func warmAt(t float64) lipgloss.Color {
	t = clamp01(t)
	switch {
	case t < 0.25:
		u := t / 0.25
		return hex(0.10+0.05*u, 0.35+0.45*u, 0.95-0.05*u)
	case t < 0.50:
		u := (t - 0.25) / 0.25
		return hex(0.15-0.05*u, 0.80+0.15*u, 0.90-0.55*u)
	case t < 0.75:
		u := (t - 0.50) / 0.25
		return hex(0.10+0.90*u, 0.95-0.05*u, 0.35-0.25*u)
	default:
		u := (t - 0.75) / 0.25
		return hex(1.0, 0.90-0.70*u, 0.10+0.05*u)
	}
}

// heatRamp is a classic receiver-waterfall gradient: near-black through blue,
// cyan, green and yellow to red. Ordered cold to hot, and used both for the
// heatmap's colour scale and for scalar values via rampColor.
var heatRamp = []lipgloss.Color{
	lipgloss.Color("233"), lipgloss.Color("17"), lipgloss.Color("18"),
	lipgloss.Color("19"), lipgloss.Color("20"), lipgloss.Color("26"),
	lipgloss.Color("32"), lipgloss.Color("38"), lipgloss.Color("44"),
	lipgloss.Color("50"), lipgloss.Color("86"), lipgloss.Color("120"),
	lipgloss.Color("155"), lipgloss.Color("191"), lipgloss.Color("227"),
	lipgloss.Color("221"), lipgloss.Color("215"), lipgloss.Color("209"),
	lipgloss.Color("203"), lipgloss.Color("197"),
}

// coolWarm runs blue -> cyan -> green -> yellow -> orange -> red. Used for
// scalar readouts like foF2 where "higher is hotter" is the whole message.
var coolWarm = []lipgloss.Color{
	lipgloss.Color("27"), lipgloss.Color("33"), lipgloss.Color("39"),
	lipgloss.Color("45"), lipgloss.Color("51"), lipgloss.Color("50"),
	lipgloss.Color("48"), lipgloss.Color("83"), lipgloss.Color("119"),
	lipgloss.Color("155"), lipgloss.Color("191"), lipgloss.Color("226"),
	lipgloss.Color("220"), lipgloss.Color("214"), lipgloss.Color("208"),
	lipgloss.Color("202"), lipgloss.Color("196"),
}

// rampColor picks from a ramp by fraction, clamped.
func rampColor(ramp []lipgloss.Color, frac float64) lipgloss.Color {
	if len(ramp) == 0 {
		return lipgloss.Color("252")
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	i := int(frac * float64(len(ramp)-1))
	if i >= len(ramp) {
		i = len(ramp) - 1
	}
	return ramp[i]
}

// scaled renders a number coloured by where it falls between lo and hi.
func scaled(v, lo, hi float64, format string) string {
	frac := 0.0
	if hi > lo {
		frac = (v - lo) / (hi - lo)
	}
	return lipgloss.NewStyle().Foreground(warmAt(frac)).
		Render(fmt.Sprintf(format, v))
}

// badge renders a filled label -- background colour, contrasting text. Reads as
// a chip rather than as another word in a sentence.
func badge(text string, bg lipgloss.Color) string {
	fg := lipgloss.Color("16") // near-black; every badge colour here is bright
	return lipgloss.NewStyle().Foreground(fg).Background(bg).Bold(true).
		Render(" " + text + " ")
}

func statusBadge(s Status) string {
	switch s {
	case StatusNVIS:
		return badge("NVIS+DX ", lipgloss.Color("47"))
	case StatusDX:
		return badge("DX ONLY ", lipgloss.Color("42"))
	case StatusMarginal:
		return badge("MARGINAL", lipgloss.Color("220"))
	case StatusES:
		return badge("SPORAD-E", lipgloss.Color("51"))
	case StatusAbsorbed:
		return badge("ABSORBED", lipgloss.Color("203"))
	case StatusClosed:
		return badge("CLOSED  ", lipgloss.Color("240"))
	}
	return badge("NO DATA ", lipgloss.Color("238"))
}

// spectrumMaxMHz is the top of the frequency axis the sounding bars are drawn
// against. 36 leaves headroom above 10m for the occasional MUF in the thirties
// without wasting half the bar on frequencies nobody works.
const spectrumMaxMHz = 36.0

// spectrumBar draws one station's usable HF spectrum as a bar.
//
// This exists because a row of five numbers -- foF2, MUF, foEs, confidence,
// age -- makes you do the propagation arithmetic yourself, for every station,
// every time. The bar does it once: you see how far up the spectrum that
// ionosphere reaches and where it stops being usable, in the same glance that
// tells you which station it is.
//
// Shading matches the BANDS tab exactly, so the two screens teach each other:
//
//	█  at or below foF2      reflects vertically -- local and DX
//	▓  foF2 .. MUF(3000)     skip only
//	░  up to MUF x 1.15      marginal; MUF is a median
//	·  above that            closed via F2
func spectrumBar(fof2, mufd, haf float64, width int) string {
	if width <= 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < width; i++ {
		f := (float64(i) + 0.5) / float64(width) * spectrumMaxMHz
		switch {
		case haf > 0 && f < haf:
			b.WriteString(lipgloss.NewStyle().Foreground(cRed).Render("▁"))
		case f <= fof2:
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("47")).Render("█"))
		case f <= mufd:
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("▓"))
		case f <= mufd*1.15:
			b.WriteString(lipgloss.NewStyle().Foreground(cYellow).Render("░"))
		default:
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("236")).Render("·"))
		}
	}
	return b.String()
}

// spectrumScale returns the tick row and the label row that sit above the bars,
// so a position on the bar can be read back as an actual band.
func spectrumScale(width int) (string, string) {
	ticks := make([]rune, width)
	labels := make([]rune, width)
	for i := range ticks {
		ticks[i] = ' '
		labels[i] = ' '
	}
	put := func(s string, at int) {
		start := at - len(s)/2
		for i, r := range s {
			if p := start + i; p >= 0 && p < width {
				labels[p] = r
			}
		}
	}
	for _, bd := range Bands {
		if bd.MHz > spectrumMaxMHz {
			continue
		}
		col := int(bd.MHz / spectrumMaxMHz * float64(width))
		if col >= width {
			col = width - 1
		}
		ticks[col] = '┬'
		// Label only every other band; all ten collide at any sane width.
		switch bd.Name {
		case "160m", "40m", "20m", "15m", "10m":
			put(strings.TrimSuffix(bd.Name, "m"), col)
		}
	}
	return stFaint.Render(string(ticks)), stFaint.Render(string(labels))
}

// sparkChars are the eight block heights used by the inline sparklines.
var sparkChars = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// shadeChar gives the waterfall texture as well as colour, so intensity is
// still legible where colour is not: -plain output, a piped dump, a colourblind
// reader, or a terminal that lost its palette. Encoding the same value twice is
// cheap and means the picture never depends on colour alone.
func shadeChar(frac float64) string {
	switch {
	case frac < 0.15:
		return "░"
	case frac < 0.40:
		return "▒"
	case frac < 0.70:
		return "▓"
	}
	return "█"
}

// inlineSpark renders one band's history, normalised to that band's OWN peak.
//
// It is deliberately about shape, not magnitude: the ACTIVITY bar next to it
// already carries the cross-band comparison, on a shared scale. Sharing a scale
// here too would waste the column -- spot counts span three orders of magnitude
// across bands, so every band except the busiest would flatline.
func inlineSpark(vals []float64, width int, c lipgloss.Color) string {
	if len(vals) == 0 || width <= 0 {
		return strings.Repeat(" ", maxInt(width, 0))
	}
	max := 0.0
	for _, v := range vals {
		if v > max {
			max = v
		}
	}
	if max <= 0 {
		return strings.Repeat(" ", width)
	}
	// Downsample to the available width by taking the max of each chunk: peaks
	// are the signal here, and averaging would flatten a short opening away.
	out := make([]rune, 0, width)
	n := len(vals)
	for i := 0; i < width; i++ {
		lo := i * n / width
		hi := (i + 1) * n / width
		if hi <= lo {
			hi = lo + 1
		}
		if lo >= n {
			out = append(out, ' ')
			continue
		}
		peak := 0.0
		for _, v := range vals[lo:minInt(hi, n)] {
			if v > peak {
				peak = v
			}
		}
		if peak <= 0 {
			out = append(out, ' ')
			continue
		}
		idx := int(peak / max * float64(len(sparkChars)-1))
		if idx >= len(sparkChars) {
			idx = len(sparkChars) - 1
		}
		out = append(out, sparkChars[idx])
	}
	return lipgloss.NewStyle().Foreground(c).Render(string(out))
}

// gauge draws a proportional bar whose colour tracks the fill. Filled cells use
// the ramp; the remainder is a dim track so the full scale stays visible.
func gauge(v, max float64, width int, ramp []lipgloss.Color) string {
	if width <= 0 {
		return ""
	}
	if max <= 0 {
		max = 1
	}
	frac := v / max
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	filled := int(frac * float64(width))
	if filled == 0 && v > 0 {
		filled = 1
	}
	var b strings.Builder
	for i := 0; i < filled; i++ {
		c := rampColor(ramp, float64(i)/float64(maxInt(width-1, 1)))
		b.WriteString(lipgloss.NewStyle().Foreground(c).Render("█"))
	}
	if rest := width - filled; rest > 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("236")).
			Render(strings.Repeat("─", rest)))
	}
	return b.String()
}

// kpColor follows the NOAA G-scale rather than a smooth ramp, because the
// thresholds are what matter: 5 is G1, where HF actually starts to suffer.
func kpColor(kp float64) lipgloss.Color {
	switch {
	case kp < 3:
		return lipgloss.Color("47") // quiet
	case kp < 4:
		return lipgloss.Color("191") // unsettled
	case kp < 5:
		return lipgloss.Color("220") // active
	case kp < 6:
		return lipgloss.Color("208") // G1
	case kp < 7:
		return lipgloss.Color("202") // G2
	}
	return lipgloss.Color("196") // G3+
}

func kpLabel(kp float64) string {
	switch {
	case kp < 3:
		return "quiet"
	case kp < 4:
		return "unsettled"
	case kp < 5:
		return "active"
	case kp < 6:
		return "G1 minor storm"
	case kp < 7:
		return "G2 moderate storm"
	case kp < 8:
		return "G3 strong storm"
	}
	return "G4+ severe storm"
}

// panel wraps content in a rounded border with a coloured title inlaid in the
// top edge.
func panel(title string, body string, w int, c lipgloss.Color) string {
	if w < 10 {
		w = 10
	}
	inner := w - 2
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(c).
		Width(inner).
		Padding(0, 1)
	rendered := style.Render(body)

	// Inlay the title into the top border line.
	lines := strings.Split(rendered, "\n")
	if len(lines) > 0 && title != "" {
		t := lipgloss.NewStyle().Foreground(c).Bold(true).Render(" " + title + " ")
		top := lines[0]
		runes := []rune(stripANSI(top))
		if len(runes) > lipgloss.Width(t)+4 {
			prefix := lipgloss.NewStyle().Foreground(c).Render("╭─")
			restLen := len(runes) - 2 - lipgloss.Width(t)
			if restLen < 0 {
				restLen = 0
			}
			suffix := lipgloss.NewStyle().Foreground(c).
				Render(strings.Repeat("─", restLen-1) + "╮")
			lines[0] = prefix + t + suffix
		}
	}
	return strings.Join(lines, "\n")
}

// stripANSI removes escape sequences so border widths can be measured.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			inEsc = true
		case inEsc && (r == 'm' || r == 'K'):
			inEsc = false
		case !inEsc:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
