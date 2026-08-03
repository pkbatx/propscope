package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/NimbleMarkets/ntcharts/barchart"
	"github.com/NimbleMarkets/ntcharts/linechart/timeserieslinechart"
	"github.com/NimbleMarkets/ntcharts/sparkline"
	"github.com/charmbracelet/lipgloss"
)

var (
	cAccent = lipgloss.Color("39")
	cDim    = lipgloss.Color("245")
	cFaint  = lipgloss.Color("240")
	cRed    = lipgloss.Color("203")
	cYellow = lipgloss.Color("220")
	cCyan   = lipgloss.Color("87")
	cGreenB = lipgloss.Color("47")
	cWhite  = lipgloss.Color("252")

	stTitle  = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	stDim    = lipgloss.NewStyle().Foreground(cDim)
	stFaint  = lipgloss.NewStyle().Foreground(cFaint)
	stWhite  = lipgloss.NewStyle().Foreground(cWhite)
	stHeader = lipgloss.NewStyle().Foreground(cDim).Bold(true)
	stErr    = lipgloss.NewStyle().Foreground(cRed).Bold(true)
	stWarn   = lipgloss.NewStyle().Foreground(cYellow)
)

// displayLoc is the QTH's wall-clock timezone, which is deliberately NOT the
// host's. Servers run UTC -- this one does, and a container will too -- while
// the operator is in Austin, so time.Local would just print Zulu twice. Set
// PROPSCOPE_TZ to move the station.
//
// The tzdata database is embedded (see the blank import in main.go) so this
// resolves identically on a distroless container with no /usr/share/zoneinfo.
var displayLoc = func() *time.Location {
	name := os.Getenv("PROPSCOPE_TZ")
	if name == "" {
		name = "America/Chicago"
	}
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return time.Local
}()

// fmtZL renders an instant as both Zulu and QTH wall time.
//
// Ham radio runs on UTC and every upstream here publishes it, but "is 20m open
// yet" is a question about local daylight. Showing one without the other means
// doing the arithmetic in your head at exactly the moment you did not want to.
func fmtZL(t time.Time) string {
	return t.UTC().Format("15:04Z") + " / " + t.In(displayLoc).Format("15:04 MST")
}

func fmtAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// section draws a coloured title bar that fills the width. Cheaper and more
// robust than boxing wide content -- no border-width arithmetic to get wrong.
func section(title, subtitle string, c lipgloss.Color, w int) string {
	head := lipgloss.NewStyle().Foreground(c).Bold(true).Render("▌ " + title)
	sub := ""
	if subtitle != "" {
		sub = stFaint.Render("  " + subtitle)
	}
	used := lipgloss.Width(head) + lipgloss.Width(sub)
	fill := w - used - 1
	if fill < 1 {
		fill = 1
	}
	return head + sub + " " +
		lipgloss.NewStyle().Foreground(lipgloss.Color("236")).
			Render(strings.Repeat("─", fill))
}

// ------------------------------------------------------------------ chrome

func (m model) chrome(body string) string {
	var b strings.Builder
	sol := m.snap.Solar

	title := lipgloss.NewStyle().Foreground(lipgloss.Color("16")).
		Background(cAccent).Bold(true).Render(" propscope ")
	left := title
	// Shed the subtitle before the numbers: on a narrow screen "HF propagation
	// · Austin TX" is the least useful thing on the line, and something has to
	// go or the header overruns and pushes the layout sideways.
	if m.w >= 92 {
		left += stDim.Render(" HF propagation · Austin TX")
	}

	right := stFaint.Render("no solar data")
	if sol.SFI6h > 0 {
		right = stFaint.Render("SFI ") +
			lipgloss.NewStyle().Foreground(rampColor(coolWarm,
				(sol.SFI6h-70)/200)).Bold(true).Render(fmt.Sprintf("%.0f", sol.SFI6h)) +
			stFaint.Render("  SSN ") +
			lipgloss.NewStyle().Foreground(rampColor(coolWarm,
				sol.SSN6h/200)).Bold(true).Render(fmt.Sprintf("%.0f", sol.SSN6h)) +
			stFaint.Render("  Kp ") +
			lipgloss.NewStyle().Foreground(kpColor(sol.Kp)).Bold(true).
				Render(fmt.Sprintf("%.1f", sol.Kp))
	}
	// The clock is the operator's, in both the timezone the hobby uses and the
	// one they actually live in.
	right += stFaint.Render("   ") + stWhite.Render(time.Now().UTC().Format("15:04Z"))
	if m.w >= 72 {
		right += stFaint.Render(" / ") +
			stDim.Render(time.Now().In(displayLoc).Format("15:04 MST"))
	}

	// If it still will not fit, drop the right-hand block entirely rather than
	// overrunning. A header one column too wide shifts every line below it.
	pad := m.w - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		right = ""
		pad = maxInt(m.w-lipgloss.Width(left), 0)
	}
	b.WriteString(left + strings.Repeat(" ", pad) + right + "\n")

	var tabs []string
	for i := tab(0); i < numTabs; i++ {
		if i == m.tab {
			tabs = append(tabs, lipgloss.NewStyle().
				Foreground(lipgloss.Color("16")).Background(cAccent).
				Bold(true).Padding(0, 1).Render(tabNames[i]))
		} else {
			tabs = append(tabs, lipgloss.NewStyle().Foreground(cFaint).
				Padding(0, 1).Render(tabNames[i]))
		}
	}
	b.WriteString(strings.Join(tabs, "") + "\n\n")
	b.WriteString(body)

	help := stFaint.Render("tab/1-5 · r refresh · ? help · q quit")
	var stat string
	switch {
	case m.lastErr != nil:
		stat = stErr.Render("ERROR: " + m.lastErr.Error())
	case m.loading:
		stat = stWarn.Render("refreshing…")
	default:
		stat = stFaint.Render("updated " + fmtAge(time.Since(m.snap.At)) + " ago")
	}
	pad = m.w - lipgloss.Width(help) - lipgloss.Width(stat)
	if pad < 1 {
		pad = 1
	}
	b.WriteString("\n" + help + strings.Repeat(" ", pad) + stat)
	return b.String()
}

func (m model) viewHelp() string {
	l := func(b, text string) string {
		return "  " + b + "  " + stDim.Render(text) + "\n"
	}
	return stTitle.Render("propscope — what the numbers mean") + "\n\n" +
		section("BAND STATUS", "", cAccent, m.w) + "\n" +
		l(statusBadge(StatusNVIS), "at or below foF2 — reflects straight down. Local AND DX.") +
		l(statusBadge(StatusDX), "above foF2, at or below MUF(3000). Skip only; dead zone around you.") +
		l(statusBadge(StatusMarginal), "within 15% above MUF. MUF is a median — opens on a good day.") +
		l(statusBadge(StatusES), "F2 cannot carry it, sporadic E can (up to ~5× foEs).") +
		l(statusBadge(StatusAbsorbed), "below the D-region absorption limit.") +
		l(statusBadge(StatusClosed), "above MUF with no Es.") +
		"\n" + section("SOURCES", "", cCyan, m.w) + "\n" +
		stDim.Render(
			"  spots/band   wspr.live — every WSPR reception report worldwide, 10-min buckets.\n"+
				"  SFI / SSN    prop.kc2g.com effective sunspot number, fitted from real soundings.\n"+
				"  daily SSN    NOAA SWPC daily solar data report, 30-day history.\n"+
				"  foF2 / MUF   GIRO ionosondes via prop.kc2g.com.\n"+
				"  absorption   NOAA D-RAP, sampled at the Austin QTH.") + "\n\n" +
		stWarn.Render("  Band status is a rule of thumb from ONE remote station's sounding.") + "\n" +
		stWarn.Render("  A decision aid, not a forecast.") + "\n\n" +
		stFaint.Render("  press ? to go back")
}

// -------------------------------------------------------------------- HOME

// viewHome is the compressed everything-at-once view: what is open, what the
// sun is doing, how the day has gone, and which station said so. The other tabs
// exist to expand one of these four; nothing here should need scrolling.
func (m model) viewHome() string {
	if len(m.snap.Spots) == 0 && len(m.snap.ESSN) == 0 {
		return "\n" + stDim.Render("  waiting for the collector's first run…") + "\n"
	}

	// Side by side needs 40 for the band table and 30 for the solar panel plus
	// a gutter. Below that they are stacked instead of being squeezed into
	// something neither of them fits in -- which is what a phone gets.
	var top string
	if m.w >= 74 {
		leftW := m.w * 62 / 100
		if leftW < 40 {
			leftW = 40
		}
		rightW := m.w - leftW - 2
		top = lipgloss.JoinHorizontal(lipgloss.Top,
			m.homeBands(leftW), "  ", m.homeSolar(rightW))
	} else {
		top = m.homeBands(m.w) + "\n" + m.homeSolar(m.w)
	}

	var b strings.Builder
	b.WriteString(top + "\n")

	// A short waterfall: enough to read today's shape without the detail tab.
	if len(m.snap.HistTimes) >= 2 {
		gridW := m.w - 9
		if gridW > len(m.snap.HistTimes) {
			gridW = len(m.snap.HistTimes)
		}
		if gridW > 20 {
			span := m.snap.HistTimes[len(m.snap.HistTimes)-1].Sub(m.snap.HistTimes[0])
			b.WriteString(section("24H WATERFALL",
				fmt.Sprintf("%s · press 3 to expand", fmtAge(span)), cAccent, m.w) + "\n")
			for _, r := range m.buildWaterfall(gridW) {
				b.WriteString(lipgloss.NewStyle().Foreground(bandColor(r.Band.WSPR)).
					Bold(true).Render(fmt.Sprintf("%6s ", r.Band.Name)) + r.Cells + "\n")
			}
			b.WriteString(m.waterfallAxis(gridW, 7, true) + "\n")
		}
	}

	// Solar trend underneath, if the terminal is tall enough to take it. The
	// waterfall answers "what has today looked like"; these answer "and is the
	// sun trending up or down underneath it", which is the other half of
	// deciding whether to bother tomorrow.
	used := strings.Count(b.String(), "\n") + 6 // + chrome and footer
	if m.h-used >= 9 {
		b.WriteString("\n" + m.homeCharts(m.w, minInt(m.h-used-2, 12)))
	}
	return b.String()
}

// tsChart builds a braille time-series chart over one series.
func tsChart(pts []timeserieslinechart.TimePoint, w, h int, c lipgloss.Color) string {
	if len(pts) < 2 || w < 10 || h < 3 {
		return ""
	}
	ch := timeserieslinechart.New(w, h,
		timeserieslinechart.WithAxesStyles(
			lipgloss.NewStyle().Foreground(lipgloss.Color("236")),
			lipgloss.NewStyle().Foreground(cFaint)))
	ch.SetStyle(lipgloss.NewStyle().Foreground(c))

	minT, maxT := pts[0].Time, pts[0].Time
	minY, maxY := pts[0].Value, pts[0].Value
	for _, p := range pts {
		ch.Push(p)
		if p.Time.Before(minT) {
			minT = p.Time
		}
		if p.Time.After(maxT) {
			maxT = p.Time
		}
		if p.Value < minY {
			minY = p.Value
		}
		if p.Value > maxY {
			maxY = p.Value
		}
	}
	// Pad so the trace never sits flat on an axis, and so a dead-flat series
	// still gets a sane range rather than a zero-height one.
	span := maxY - minY
	if span < 1 {
		span = 1
	}
	ch.SetViewTimeAndYRange(minT, maxT, minY-span*0.12, maxY+span*0.12)
	ch.DrawBraille()
	return ch.View()
}

// homeCharts puts the two solar trends side by side under the waterfall.
func (m model) homeCharts(w, h int) string {
	leftW := w/2 - 1
	rightW := w - leftW - 2
	if leftW < 20 || rightW < 20 {
		return ""
	}
	chartH := h - 1

	// --- effective SSN, 7 days ---------------------------------------------
	var ssn []timeserieslinechart.TimePoint
	for _, p := range m.snap.ESSN {
		if p.Span == "6h" {
			ssn = append(ssn, timeserieslinechart.TimePoint{Time: p.T, Value: p.SSN})
		}
	}
	lo, hi := 0.0, 0.0
	for i, p := range ssn {
		if i == 0 || p.Value < lo {
			lo = p.Value
		}
		if i == 0 || p.Value > hi {
			hi = p.Value
		}
	}
	left := section("EFFECTIVE SSN",
		fmt.Sprintf("7d · %.0f–%.0f · now %.1f", lo, hi, m.snap.Solar.SSN6h),
		cGreenB, leftW) + "\n" +
		tsChart(ssn, leftW, chartH, cGreenB)

	// --- 10.7cm flux, 30 days ------------------------------------------------
	var flux []timeserieslinechart.TimePoint
	for _, d := range m.snap.Daily {
		flux = append(flux, timeserieslinechart.TimePoint{Time: d.D, Value: d.Flux})
	}
	flo, fhi := 0.0, 0.0
	for i, p := range flux {
		if i == 0 || p.Value < flo {
			flo = p.Value
		}
		if i == 0 || p.Value > fhi {
			fhi = p.Value
		}
	}
	right := section("10.7cm FLUX",
		fmt.Sprintf("30d · %.0f–%.0f · now %.0f", flo, fhi, m.snap.Solar.DailyFlux),
		cCyan, rightW) + "\n" +
		tsChart(flux, rightW, chartH, cCyan)

	return lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right)
}

func (m model) homeBands(w int) string {
	var b strings.Builder
	if len(m.snap.Spots) == 0 {
		return section("BANDS NOW", "no data", cAccent, w) + "\n"
	}

	spotsByBand := map[int]int64{}
	for _, s := range m.snap.Spots {
		spotsByBand[s.Band] = s.Spots
	}
	b.WriteString(section("BANDS NOW", fmtZL(m.snap.SpotBucket), cAccent, w) + "\n")

	barW := w - 26
	if barW < 8 {
		barW = 8
	}
	var bars []barchart.BarData
	for _, bd := range Bands {
		bars = append(bars, barchart.BarData{
			Label: bd.Name,
			Values: []barchart.BarValue{{
				Name:  "spots",
				Value: float64(spotsByBand[bd.WSPR]),
				Style: lipgloss.NewStyle().Foreground(bandColor(bd.WSPR)),
			}},
		})
	}
	bc := barchart.New(barW, len(bars),
		barchart.WithHorizontalBars(), barchart.WithNoAxis(),
		barchart.WithNoAutoBarWidth(), barchart.WithBarWidth(1),
		barchart.WithBarGap(0))
	bc.PushAll(bars)
	bc.Draw()
	lines := strings.Split(bc.View(), "\n")

	ref, _ := m.snap.Reference()
	for i, bd := range Bands {
		bar := ""
		if i < len(lines) {
			bar = lines[i]
		}
		n := spotsByBand[bd.WSPR]
		count := stFaint.Render(fmt.Sprintf("%6d", n))
		if n > 0 {
			count = stWhite.Render(fmt.Sprintf("%6d", n))
		}
		b.WriteString(" " +
			lipgloss.NewStyle().Foreground(bandColor(bd.WSPR)).Bold(true).
				Render(fmt.Sprintf("%5s", bd.Name)) + " " +
			bar + " " + count + " " +
			statusBadge(Classify(bd, ref, m.snap.Solar.HAF)) + "\n")
	}
	return b.String()
}

func (m model) homeSolar(w int) string {
	var b strings.Builder
	sol := m.snap.Solar
	gw := w - 24
	if gw < 8 {
		gw = 8
	}

	b.WriteString(section("SOLAR", fmtAge(time.Since(sol.ESSNTime))+" old", cYellow, w) + "\n")
	row := func(label string, v float64, max float64, ramp []lipgloss.Color, note string) {
		b.WriteString(" " + stDim.Render(fmt.Sprintf("%-5s", label)) +
			lipgloss.NewStyle().Foreground(rampColor(ramp, v/max)).Bold(true).
				Render(fmt.Sprintf("%6.1f", v)) + " " +
			gauge(v, max, gw, ramp) + " " + note + "\n")
	}
	row("SFI", sol.SFI6h, 300, coolWarm, stFaint.Render(fmt.Sprintf("24h %.0f", sol.SFI24h)))
	row("SSN", sol.SSN6h, 200, coolWarm, stFaint.Render(fmt.Sprintf("24h %.0f", sol.SSN24h)))

	kpRamp := []lipgloss.Color{
		lipgloss.Color("47"), lipgloss.Color("191"), lipgloss.Color("220"),
		lipgloss.Color("208"), lipgloss.Color("202"), lipgloss.Color("196")}
	b.WriteString(" " + stDim.Render(fmt.Sprintf("%-5s", "Kp")) +
		lipgloss.NewStyle().Foreground(kpColor(sol.Kp)).Bold(true).
			Render(fmt.Sprintf("%6.2f", sol.Kp)) + " " +
		gauge(sol.Kp, 9, gw, kpRamp) + " " +
		lipgloss.NewStyle().Foreground(kpColor(sol.Kp)).Render(kpLabel(sol.Kp)) + "\n")

	b.WriteString(" " + stDim.Render(fmt.Sprintf("%-5s", "absrb")) +
		lipgloss.NewStyle().Foreground(rampColor(coolWarm, sol.HAF/20)).Bold(true).
			Render(fmt.Sprintf("%6.1f", sol.HAF)) + " " +
		gauge(sol.HAF, 20, gw, coolWarm) + " " + stFaint.Render("MHz LUF") + "\n")

	// This panel can be as narrow as ~34 columns when the layout splits, so the
	// daily summary needs a short form rather than a fixed one that overruns.
	daily := fmt.Sprintf(" SWPC %s: flux %.0f · SSN %d · C%d M%d X%d",
		sol.Day.Format("Jan 02"), sol.DailyFlux, sol.DailySSN,
		sol.XrayC, sol.XrayM, sol.XrayX)
	if len([]rune(daily)) > w {
		daily = fmt.Sprintf(" SWPC: flux %.0f · SSN %d", sol.DailyFlux, sol.DailySSN)
	}
	b.WriteString(stFaint.Render(daily) + "\n")

	// --- the station the band column speaks for ------------------------------
	b.WriteString("\n" + section("IONOSPHERE", "reference sounding", cCyan, w) + "\n")
	ref, good := m.snap.Reference()
	if ref == nil {
		b.WriteString(" " + stErr.Render("no usable sounding in 24h") + "\n")
		return b.String()
	}
	name := strings.TrimSpace(ref.Name)
	if max := w - 12; len(name) > max && max > 3 {
		name = name[:max]
	}
	b.WriteString(" " + lipgloss.NewStyle().Foreground(cGreenB).Bold(true).
		Render(ref.Code) + " " + stDim.Render(name) + "\n")
	line := fmt.Sprintf("%.0f km away · sounded %s ago", ref.KM, fmtAge(ref.Age()))
	if len([]rune(line))+1 > w {
		line = fmt.Sprintf("%.0f km · %s", ref.KM, fmtAge(ref.Age()))
	}
	b.WriteString(" " + stFaint.Render(line) + "\n")
	b.WriteString(" " + stFaint.Render("foF2 ") + scaled(ref.FoF2, 2, 14, "%5.2f") +
		stFaint.Render("  MUF ") + scaled(ref.MUFD, 5, 40, "%5.2f") +
		stFaint.Render("  foEs ") + scaled(ref.FoEs, 0, 10, "%5.2f") + "\n")
	if !good {
		b.WriteString(" " + stWarn.Render(fmt.Sprintf("⚠ low confidence (cs=%.0f)", ref.CS)) + "\n")
	}
	// The dead Austin sounder is deliberately NOT mentioned here. It is a fixed
	// fact that will not change, and a permanent warning is just noise on a
	// screen you look at every day. The distance to the reference station above
	// already says everything actionable; the IONOSPHERE tab carries the why.
	return b.String()
}

// ------------------------------------------------------------------- BANDS

func (m model) viewBands() string {
	if len(m.snap.Spots) == 0 {
		return "\n" + stDim.Render("  no WSPR data yet — the collector fills this every 10 minutes.") + "\n"
	}
	var b strings.Builder

	spotsByBand := map[int]int64{}
	snrByBand := map[int]float64{}
	kmByBand := map[int]float64{}
	for _, s := range m.snap.Spots {
		spotsByBand[s.Band] = s.Spots
		snrByBand[s.Band] = s.AvgSNR
		kmByBand[s.Band] = s.MaxKM
	}

	bucket := m.snap.SpotBucket.UTC().Format("15:04Z")
	b.WriteString(section("LIVE BAND ACTIVITY",
		fmt.Sprintf("worldwide WSPR · bucket ending %s · %s ago",
			bucket, fmtAge(time.Since(m.snap.SpotBucket))), cAccent, m.w) + "\n")

	ref, refGood := m.snap.Reference()

	// Bands stay in FREQUENCY order rather than sorted by traffic: it keeps the
	// spectrum colours running top to bottom as a gradient, and it means a band
	// does not jump rows between refreshes.
	// 59 = every fixed column and separator in the row format below.
	barW := m.w - 59
	if barW < 12 {
		barW = 12
	}
	sparkW := 14

	var bars []barchart.BarData
	for _, bd := range Bands {
		bars = append(bars, barchart.BarData{
			Label: bd.Name,
			Values: []barchart.BarValue{{
				Name:  "spots",
				Value: float64(spotsByBand[bd.WSPR]),
				Style: lipgloss.NewStyle().Foreground(bandColor(bd.WSPR)),
			}},
		})
	}
	bc := barchart.New(barW, len(bars),
		barchart.WithHorizontalBars(),
		barchart.WithNoAxis(),
		barchart.WithNoAutoBarWidth(),
		barchart.WithBarWidth(1),
		barchart.WithBarGap(0))
	bc.PushAll(bars)
	bc.Draw()
	barLines := strings.Split(bc.View(), "\n")

	b.WriteString(stHeader.Render(fmt.Sprintf("  %-5s %-*s %7s  %-10s %6s %7s  %s",
		"BAND", barW, "ACTIVITY", "SPOTS", "STATUS", "SNR", "BEST KM", "24H TREND")) + "\n")

	for i, bd := range Bands {
		bar := ""
		if i < len(barLines) {
			bar = barLines[i]
		}
		st := Classify(bd, ref, m.snap.Solar.HAF)
		n := spotsByBand[bd.WSPR]

		name := lipgloss.NewStyle().Foreground(bandColor(bd.WSPR)).Bold(true).
			Render(fmt.Sprintf("%5s", bd.Name))

		count := stFaint.Render(fmt.Sprintf("%7d", n))
		if n > 0 {
			count = stWhite.Render(fmt.Sprintf("%7d", n))
		}

		snr := stFaint.Render(fmt.Sprintf("%6s", "—"))
		if n > 0 {
			snr = scaled(snrByBand[bd.WSPR], -30, 0, "%6.1f")
		}
		km := stFaint.Render(fmt.Sprintf("%7s", "—"))
		if kmByBand[bd.WSPR] > 0 {
			km = stDim.Render(fmt.Sprintf("%7.0f", kmByBand[bd.WSPR]))
		}

		spark := inlineSpark(m.snap.Hist[bd.WSPR], sparkW, bandColor(bd.WSPR))

		b.WriteString(fmt.Sprintf("  %s %s %s  %s %s %s  %s\n",
			name, bar, count, statusBadge(st), snr, km, spark))
	}

	// --- the model behind the status column ---------------------------------
	b.WriteString("\n")
	if ref == nil {
		b.WriteString(stErr.Render("  no usable sounding in the last 24h — status column is blank") + "\n")
		return b.String()
	}
	b.WriteString(section("PROPAGATION MODEL",
		fmt.Sprintf("%s · %s · %.0f km · %s old",
			ref.Code, strings.TrimSpace(ref.Name), ref.KM, fmtAge(ref.Age())),
		cCyan, m.w) + "\n")

	b.WriteString("  " +
		stFaint.Render("foF2 ") + scaled(ref.FoF2, 2, 14, "%.2f") + stFaint.Render(" MHz") +
		stFaint.Render("   MUF(3000) ") + scaled(ref.MUFD, 5, 40, "%.2f") + stFaint.Render(" MHz") +
		stFaint.Render("   foEs ") + scaled(ref.FoEs, 0, 10, "%.2f") + stFaint.Render(" MHz") +
		stFaint.Render("   absorption ") + scaled(m.snap.Solar.HAF, 0, 20, "%.1f") + stFaint.Render(" MHz") + "\n")
	if !refGood {
		b.WriteString(stWarn.Render(fmt.Sprintf(
			"  ⚠ low autoscaling confidence (cs=%.0f) — indicative only", ref.CS)) + "\n")
	}
	return b.String()
}

// --------------------------------------------------------------- WATERFALL

// waterfallRow is one band's rendered strip plus the peak it was scaled to.
type waterfallRow struct {
	Band  Band
	Cells string
	Peak  float64
}

// buildWaterfall renders the band x time grid.
//
// Hand-rendered rather than using ntcharts' heatmap widget. That widget is
// built for continuous XY functions: it wraps a linechart, reserves the canvas
// edges for axes, and expects points sampled across GraphWidth() x
// GraphHeight() -- and PushAllMatrixRow maps x=row, y=column, transposed from
// the obvious reading. Points landing outside the graph area are dropped
// silently, so the failure mode is a blank chart. Here the Y axis is eleven
// NAMED bands, and resampling them to whatever GraphHeight() happens to be
// would decouple the rows from their labels.
//
// Bands run high at the TOP, the way every receiver waterfall is drawn. Each
// row is normalised to ITS OWN peak: on a shared scale 20m (peaking near 24000
// spots a bucket) saturates all day while 6m (50) never leaves the bottom, and
// the diurnal structure that is the whole point disappears.
func (m model) buildWaterfall(gridW int) []waterfallRow {
	n := len(m.snap.HistTimes)
	if n == 0 || gridW <= 0 {
		return nil
	}
	out := make([]waterfallRow, 0, len(Bands))
	for i := len(Bands) - 1; i >= 0; i-- {
		bd := Bands[i]
		src := m.snap.Hist[bd.WSPR]
		row := make([]float64, gridW)
		peak := 0.0
		for x := 0; x < gridW; x++ {
			lo := x * n / gridW
			hi := (x + 1) * n / gridW
			if hi <= lo {
				hi = lo + 1
			}
			p := 0.0
			for j := lo; j < hi && j < len(src); j++ {
				if src[j] > p {
					p = src[j]
				}
			}
			row[x] = p
			if p > peak {
				peak = p
			}
		}
		var cells strings.Builder
		for _, v := range row {
			if v <= 0 || peak <= 0 {
				cells.WriteString(lipgloss.NewStyle().
					Foreground(lipgloss.Color("233")).Render("\u00b7"))
				continue
			}
			// Linear, not log: the row is already normalised to its own peak,
			// so linear spreads that band's quiet-to-open range across the
			// whole ramp. Log would bunch it into the top third again.
			frac := v / peak
			cells.WriteString(lipgloss.NewStyle().
				Foreground(heatAt(frac)).Render(shadeChar(frac)))
		}
		out = append(out, waterfallRow{Band: bd, Cells: cells.String(), Peak: peak})
	}
	return out
}

// waterfallAxis lays three timestamps under a grid of the given width.
func (m model) waterfallAxis(gridW int, indent int, withLocal bool) string {
	n := len(m.snap.HistTimes)
	if n < 2 {
		return ""
	}
	f := func(t time.Time) string {
		if withLocal {
			return fmtZL(t)
		}
		return t.UTC().Format("15:04Z")
	}
	first, mid, last := f(m.snap.HistTimes[0]), f(m.snap.HistTimes[n/2]), f(m.snap.HistTimes[n-1])
	pad := gridW - len(first) - len(mid) - len(last)
	if pad < 2 {
		pad = 2
	}
	return strings.Repeat(" ", indent) + stFaint.Render(
		first+strings.Repeat(" ", pad/2)+mid+
			strings.Repeat(" ", pad-pad/2)+last)
}

func (m model) viewWaterfall() string {
	if len(m.snap.HistTimes) < 2 {
		return "\n" + stDim.Render("  not enough history yet for the waterfall.") + "\n"
	}
	var b strings.Builder

	span := m.snap.HistTimes[len(m.snap.HistTimes)-1].Sub(m.snap.HistTimes[0])
	b.WriteString(section("PROPAGATION WATERFALL",
		fmt.Sprintf("spots per band \u00b7 %s of history \u00b7 %d buckets",
			fmtAge(span), len(m.snap.HistTimes)), cAccent, m.w) + "\n")

	gridW := m.w - 19
	if gridW < 20 {
		gridW = 20
	}
	if gridW > len(m.snap.HistTimes) {
		gridW = len(m.snap.HistTimes)
	}

	for _, r := range m.buildWaterfall(gridW) {
		peak := stFaint.Render("      \u2014")
		if r.Peak > 0 {
			peak = stDim.Render(fmt.Sprintf("%7.0f", r.Peak))
		}
		b.WriteString(lipgloss.NewStyle().Foreground(bandColor(r.Band.WSPR)).Bold(true).
			Render(fmt.Sprintf("%6s ", r.Band.Name)) + r.Cells + peak + "\n")
	}
	b.WriteString(m.waterfallAxis(gridW, 7, true) + "\n")

	b.WriteString("\n  " + stFaint.Render("quiet "))
	// A wider legend now that the ramp is continuous rather than 20 steps.
	const legendW = 28
	for i := 0; i < legendW; i++ {
		f := float64(i) / float64(legendW-1)
		b.WriteString(lipgloss.NewStyle().Foreground(heatAt(f)).Render(shadeChar(f)))
	}
	legend := " busy   each band scaled to its own 24h peak"
	if m.w < 80 {
		legend = " busy"
	}
	b.WriteString(stFaint.Render(legend) + "\n")
	if m.w >= 100 {
		b.WriteString(stFaint.Render(
			"  higher bands on top \u00b7 one column per time bucket, oldest left \u00b7 "+
				"right column is that band's peak spots/10min") + "\n")
	} else {
		b.WriteString(stFaint.Render("  higher bands on top \u00b7 oldest left") + "\n")
	}
	return b.String()
}

// ------------------------------------------------------------------- SOLAR

func (m model) viewSolar() string {
	if len(m.snap.ESSN) == 0 {
		return "\n" + stDim.Render("  no solar data yet.") + "\n"
	}
	var b strings.Builder
	sol := m.snap.Solar

	b.WriteString(section("SOLAR DRIVERS",
		fmt.Sprintf("effective values from ionosonde assimilation · %s old",
			fmtAge(time.Since(sol.ESSNTime))), cYellow, m.w) + "\n")

	gw := m.w - 46
	if gw < 12 {
		gw = 12
	}

	big := func(v float64) string {
		return lipgloss.NewStyle().Foreground(rampColor(coolWarm, v/200)).Bold(true).
			Render(fmt.Sprintf("%6.1f", v))
	}
	b.WriteString("  " + stDim.Render("solar flux  SFI ") + big(sol.SFI6h) + "  " +
		gauge(sol.SFI6h, 300, gw, coolWarm) +
		stFaint.Render(fmt.Sprintf("  24h avg %.0f", sol.SFI24h)) + "\n")
	b.WriteString("  " + stDim.Render("sunspots    SSN ") + big(sol.SSN6h) + "  " +
		gauge(sol.SSN6h, 200, gw, coolWarm) +
		stFaint.Render(fmt.Sprintf("  24h avg %.0f", sol.SSN24h)) + "\n")
	b.WriteString("  " + stDim.Render("geomagnetic Kp  ") +
		lipgloss.NewStyle().Foreground(kpColor(sol.Kp)).Bold(true).
			Render(fmt.Sprintf("%6.2f", sol.Kp)) + "  " +
		gauge(sol.Kp, 9, gw, []lipgloss.Color{
			lipgloss.Color("47"), lipgloss.Color("191"), lipgloss.Color("220"),
			lipgloss.Color("208"), lipgloss.Color("202"), lipgloss.Color("196")}) +
		"  " + lipgloss.NewStyle().Foreground(kpColor(sol.Kp)).Render(kpLabel(sol.Kp)) + "\n")

	absC := cGreenB
	if sol.HAF > 0 {
		absC = rampColor(coolWarm, sol.HAF/20)
	}
	// This row's trailing note is far longer than the others', so it gets its
	// own gauge width rather than overflowing the line.
	absNote := "  MHz cutoff at the QTH (0 = no D layer, i.e. night)"
	if m.w < 110 {
		absNote = "  MHz LUF at QTH"
	}
	absGW := m.w - 26 - len(absNote)
	if absGW < 8 {
		absGW = 8
	}
	if absGW > gw {
		absGW = gw
	}
	b.WriteString("  " + stDim.Render("absorption      ") +
		lipgloss.NewStyle().Foreground(absC).Bold(true).
			Render(fmt.Sprintf("%6.1f", sol.HAF)) + "  " +
		gauge(sol.HAF, 20, absGW, coolWarm) +
		stFaint.Render(absNote) + "\n")

	off := fmt.Sprintf(
		"  official SWPC daily for %s: flux %.0f · sunspot number %d · flares C%d M%d X%d",
		sol.Day.Format("Jan 02"), sol.DailyFlux, sol.DailySSN,
		sol.XrayC, sol.XrayM, sol.XrayX)
	if len([]rune(off)) > m.w {
		off = fmt.Sprintf("  SWPC %s: flux %.0f · SSN %d · C%d M%d X%d",
			sol.Day.Format("Jan 02"), sol.DailyFlux, sol.DailySSN,
			sol.XrayC, sol.XrayM, sol.XrayX)
	}
	b.WriteString(stFaint.Render(off) + "\n")

	// --- effective SSN over the last week ------------------------------------
	chartH := m.h - 26
	if chartH < 6 {
		chartH = 6
	}
	if chartH > 14 {
		chartH = 14
	}
	chartW := m.w - 2
	if chartW < 30 {
		chartW = 30
	}

	b.WriteString("\n" + section("EFFECTIVE SSN", "7 days · "+
		lipgloss.NewStyle().Foreground(cGreenB).Render("━ 6h")+
		stFaint.Render(" / ")+
		lipgloss.NewStyle().Foreground(cAccent).Render("━ 24h"), cGreenB, m.w) + "\n")

	// Axis styling is construction-only here -- there is a WithAxesStyles
	// option but no SetAxesStyles method.
	tsl := timeserieslinechart.New(chartW, chartH,
		timeserieslinechart.WithAxesStyles(
			lipgloss.NewStyle().Foreground(lipgloss.Color("238")),
			lipgloss.NewStyle().Foreground(cFaint)))
	tsl.SetDataSetStyle("6h", lipgloss.NewStyle().Foreground(cGreenB))
	tsl.SetDataSetStyle("24h", lipgloss.NewStyle().Foreground(cAccent))

	var minT, maxT time.Time
	minY, maxY := 1e9, -1e9
	for _, p := range m.snap.ESSN {
		tsl.PushDataSet(p.Span, timeserieslinechart.TimePoint{Time: p.T, Value: p.SSN})
		if minT.IsZero() || p.T.Before(minT) {
			minT = p.T
		}
		if p.T.After(maxT) {
			maxT = p.T
		}
		if p.SSN < minY {
			minY = p.SSN
		}
		if p.SSN > maxY {
			maxY = p.SSN
		}
	}
	spanY := maxY - minY
	if spanY < 1 {
		spanY = 1
	}
	tsl.SetViewTimeAndYRange(minT, maxT, minY-spanY*0.1, maxY+spanY*0.1)
	tsl.DrawBrailleDataSets([]string{"24h", "6h"})
	b.WriteString(tsl.View() + "\n")

	// --- 30-day daily flux ---------------------------------------------------
	if len(m.snap.Daily) > 1 {
		var flux []float64
		for _, d := range m.snap.Daily {
			flux = append(flux, d.Flux)
		}
		lo, hi := flux[0], flux[0]
		for _, f := range flux {
			if f < lo {
				lo = f
			}
			if f > hi {
				hi = f
			}
		}
		// ntcharts' sparkline scales from ZERO and scrolls in from the right,
		// so a 100..161 series in a wide canvas renders as uniform three-
		// quarter bars with two thirds of the canvas blank. Size to the data
		// and plot the offset from the minimum; the printed range keeps that
		// honest.
		spanF := hi - lo
		if spanF <= 0 {
			spanF = 1
		}
		norm := make([]float64, len(flux))
		for i, f := range flux {
			norm[i] = f - lo
		}
		sl := sparkline.New(len(norm), 3,
			sparkline.WithStyle(lipgloss.NewStyle().Foreground(cCyan)),
			sparkline.WithMaxValue(spanF))
		sl.PushAll(norm)
		sl.Draw()

		b.WriteString("\n" + section("10.7cm FLUX",
			fmt.Sprintf("%d days · range %.0f–%.0f · latest %.0f",
				len(flux), lo, hi, flux[len(flux)-1]), cCyan, m.w) + "\n")
		for _, ln := range strings.Split(sl.View(), "\n") {
			b.WriteString("  " + ln + "\n")
		}
	}
	return b.String()
}

// -------------------------------------------------------------- IONOSPHERE

// Enough context to trust or distrust the band column, and no more. The full
// GIRO list ran to thirty-odd stations, most of them on other continents, which
// buried the one station that actually matters.
const ionoListLimit = 8

func (m model) viewIono() string {
	var b strings.Builder
	ref, good := m.snap.Reference()

	// --- the station the model speaks for -----------------------------------
	b.WriteString(section("REFERENCE SOUNDING", "what the BANDS tab computes from",
		cGreenB, m.w) + "\n")
	if ref == nil {
		b.WriteString(" " + stErr.Render("no usable sounding in the last 24h") + "\n")
	} else {
		b.WriteString(" " + lipgloss.NewStyle().Foreground(cGreenB).Bold(true).
			Render(ref.Code) + "  " + stWhite.Render(strings.TrimSpace(ref.Name)) + "\n")
		conf := fmt.Sprintf("confidence %.0f", ref.CS)
		if ref.CS < 0 {
			conf = "not autoscaled"
		}
		b.WriteString(" " + stFaint.Render(fmt.Sprintf(
			"%.0f km from Austin \u00b7 sounded %s \u00b7 %s ago \u00b7 %s",
			ref.KM, fmtZL(ref.T), fmtAge(ref.Age()), conf)) + "\n")
		if !good {
			b.WriteString(" " + stWarn.Render(
				"\u26a0 low autoscaling confidence \u2014 treat the band column as indicative") + "\n")
		}
		b.WriteString("\n")
		val := func(label string, v float64, lo, hi float64, unit, why string) {
			b.WriteString("   " + stDim.Render(fmt.Sprintf("%-8s", label)) +
				scaled(v, lo, hi, "%6.2f") + stFaint.Render(" "+unit+"   "+why) + "\n")
		}
		val("foF2", ref.FoF2, 2, 14, "MHz", "highest frequency that reflects straight up")
		val("MUF3000", ref.MUFD, 5, 40, "MHz", "max usable for one 3000 km hop")
		val("foEs", ref.FoEs, 0, 10, "MHz", "sporadic-E; carries ~5x this obliquely")
		if ref.HmF2 > 0 {
			val("hmF2", ref.HmF2, 200, 450, "km ", "height of the F2 peak")
		}
		b.WriteString("   " + stDim.Render(fmt.Sprintf("%-8s", "absorb")) +
			scaled(m.snap.Solar.HAF, 0, 20, "%6.2f") +
			stFaint.Render(" MHz   D-region cutoff, sampled AT Austin") + "\n")
	}

	// --- the short list ------------------------------------------------------
	if len(m.snap.Soundings) > 0 {
		shown := minInt(len(m.snap.Soundings), ionoListLimit)
		b.WriteString("\n" + section("NEAREST LIVE SOUNDERS",
			fmt.Sprintf("%d of %d reporting in the last 24h", shown, len(m.snap.Soundings)),
			cCyan, m.w) + "\n")

		// Layout: identity on the left, the spectrum bar carrying the actual
		// content in the middle, exact numbers on the right for when the bar is
		// not enough. Fixed columns so the bars line up into a single block the
		// eye can compare down, which is the entire point of drawing them.
		const idW = 34
		barW := m.w - idW - 26
		if barW < 18 {
			barW = 18
		}
		if barW > 40 {
			barW = 40
		}

		// Every row is: marker(2) + space + ident(idW) + space + bar. The scale
		// and header must start at exactly that offset or the ticks lie about
		// which band a column is.
		const barCol = 2 + 1 + idW + 1
		ticks, labels := spectrumScale(barW)
		pad := strings.Repeat(" ", barCol)
		b.WriteString(pad + labels + "\n")
		b.WriteString(pad + ticks + "\n")
		b.WriteString(stHeader.Render(fmt.Sprintf("   %-*s ", idW, "STATION")) +
			strings.Repeat(" ", barW) +
			stHeader.Render(fmt.Sprintf(" %6s %6s %7s", "foF2", "MUF", "AGE")) + "\n")

		for i, s := range m.snap.Soundings {
			if i >= shown {
				break
			}
			isRef := ref != nil && s.Code == ref.Code

			marker, nameSt := "  ", stDim
			if isRef {
				marker = lipgloss.NewStyle().Foreground(cGreenB).Bold(true).Render(" \u25b8")
				nameSt = lipgloss.NewStyle().Foreground(cGreenB).Bold(true)
			}

			// One identity column: name, then distance, so the eye reads
			// "which station, how far" as a single phrase instead of hopping
			// between two widely separated columns.
			name := strings.TrimSpace(s.Name)
			dist := fmt.Sprintf("%.0f km", s.KM)
			room := idW - len(dist) - 2
			if len(name) > room && room > 1 {
				name = name[:room-1] + "\u2026"
			}
			ident := fmt.Sprintf("%-*s %s", room, name, dist)

			ageSt := lipgloss.NewStyle().Foreground(rampColor(
				[]lipgloss.Color{cGreenB, lipgloss.Color("191"), cYellow,
					lipgloss.Color("245"), cFaint}, s.Age().Hours()/12))

			b.WriteString(marker + " " +
				nameSt.Render(ident) + " " +
				spectrumBar(s.FoF2, s.MUFD, m.snap.Solar.HAF, barW) +
				" " + scaled(s.FoF2, 2, 14, "%6.2f") +
				" " + scaled(s.MUFD, 5, 40, "%6.1f") +
				" " + ageSt.Render(fmt.Sprintf("%7s", fmtAge(s.Age()))) + "\n")
		}

		b.WriteString("\n  " +
			lipgloss.NewStyle().Foreground(lipgloss.Color("47")).Render("\u2588") +
			stFaint.Render(" NVIS + DX   ") +
			lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("\u2593") +
			stFaint.Render(" DX only   ") +
			lipgloss.NewStyle().Foreground(cYellow).Render("\u2591") +
			stFaint.Render(" marginal   ") +
			lipgloss.NewStyle().Foreground(lipgloss.Color("236")).Render("\u00b7") +
			stFaint.Render(" closed") + "\n")

		if len(m.snap.Soundings) > shown {
			b.WriteString(stFaint.Render(fmt.Sprintf(
				"  %d further stations omitted, all beyond %.0f km",
				len(m.snap.Soundings)-shown, m.snap.Soundings[shown-1].KM)) + "\n")
		}
		// One quiet line, not a section: the dead local sounder is a permanent
		// fact, so it belongs as a footnote here rather than as a standing
		// warning on the dashboard.
		if m.snap.AustinKnown {
			note := fmt.Sprintf(
				"  AU930 Austin TX (15 km) has not reported for %s \u2014 hence the distance above.",
				fmtAge(m.snap.AustinAge))
			if len([]rune(note)) > m.w {
				note = fmt.Sprintf("  AU930 Austin TX silent %s", fmtAge(m.snap.AustinAge))
			}
			b.WriteString(stFaint.Render(note) + "\n")
		}
	}
	return b.String()
}
