package main

import (
	"fmt"
	"html"
	"io"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Static HTML rendering.
//
// The same View() the TUI draws, converted to HTML. Not a second frontend: one
// set of layout code, one set of colours, one place a bug can live. A cron job
// writes the output and any web server serves it, so the published page needs
// no websocket, no process per viewer and no runtime at all.

// ansiToHTML converts a rendered frame into HTML, translating SGR sequences
// into spans.
//
// It handles only the subset propscope emits -- reset, bold, and 24-bit or
// indexed foreground/background -- because the input is our own output rather
// than arbitrary terminal traffic. Anything unrecognised is dropped rather than
// guessed at, which is the safe direction: a missing colour is a cosmetic bug,
// a mis-parsed escape leaking into the page is a broken document.
func ansiToHTML(s string) string {
	var b strings.Builder
	open := false

	closeSpan := func() {
		if open {
			b.WriteString("</span>")
			open = false
		}
	}

	for i := 0; i < len(s); {
		if s[i] != 0x1b {
			// Escape the text itself; the frames contain no user input, but a
			// station name arrives from an upstream API and is not ours.
			j := strings.IndexByte(s[i:], 0x1b)
			if j < 0 {
				b.WriteString(html.EscapeString(s[i:]))
				break
			}
			b.WriteString(html.EscapeString(s[i : i+j]))
			i += j
			continue
		}
		// Expect ESC [ ... m
		if i+1 >= len(s) || s[i+1] != '[' {
			i++
			continue
		}
		end := strings.IndexByte(s[i:], 'm')
		if end < 0 {
			break
		}
		params := s[i+2 : i+end]
		i += end + 1

		var fg, bg string
		bold := false
		reset := false

		f := strings.Split(params, ";")
		for k := 0; k < len(f); k++ {
			switch f[k] {
			case "", "0":
				reset = true
			case "1":
				bold = true
			case "38", "48":
				isFG := f[k] == "38"
				if k+1 >= len(f) {
					break
				}
				switch f[k+1] {
				case "2": // 24-bit
					if k+4 < len(f) {
						c := fmt.Sprintf("#%02x%02x%02x",
							atoi(f[k+2]), atoi(f[k+3]), atoi(f[k+4]))
						if isFG {
							fg = c
						} else {
							bg = c
						}
						k += 4
					}
				case "5": // indexed
					if k+2 < len(f) {
						c := xterm256(atoi(f[k+2]))
						if isFG {
							fg = c
						} else {
							bg = c
						}
						k += 2
					}
				}
			}
		}

		closeSpan()
		if reset && fg == "" && bg == "" && !bold {
			continue
		}
		var style []string
		if fg != "" {
			style = append(style, "color:"+fg)
		}
		if bg != "" {
			style = append(style, "background:"+bg)
		}
		if bold {
			style = append(style, "font-weight:700")
		}
		if len(style) > 0 {
			b.WriteString(`<span style="` + strings.Join(style, ";") + `">`)
			open = true
		}
	}
	closeSpan()
	return b.String()
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// xterm256 maps an indexed colour to hex: 16 system colours, a 6x6x6 cube, then
// 24 greys. Needed because the identity colours (band spectrum, status badges)
// are indexed by design and never become 24-bit.
func xterm256(i int) string {
	sys := []string{
		"#000000", "#800000", "#008000", "#808000", "#000080", "#800080",
		"#008080", "#c0c0c0", "#808080", "#ff0000", "#00ff00", "#ffff00",
		"#0000ff", "#ff00ff", "#00ffff", "#ffffff",
	}
	switch {
	case i < 0 || i > 255:
		return "#cccccc"
	case i < 16:
		return sys[i]
	case i < 232:
		n := i - 16
		lv := []int{0, 95, 135, 175, 215, 255}
		return fmt.Sprintf("#%02x%02x%02x", lv[n/36], lv[(n/6)%6], lv[n%6])
	default:
		g := 8 + (i-232)*10
		return fmt.Sprintf("#%02x%02x%02x", g, g, g)
	}
}

// WriteHTML renders every tab at two widths and emits a standalone page.
//
// Two widths because a 116-column monospace block is unreadable on a phone and
// unavoidably so -- you cannot reflow a waterfall. A CSS media query picks the
// narrow rendering on small screens, which costs a second render and solves it
// completely.
func WriteHTML(w io.Writer, m model, wide, narrow, height int, refreshSec int) error {
	render := func(width int) string {
		mm := m
		mm.w, mm.h = width, height
		var out strings.Builder
		for t := tab(0); t < numTabs; t++ {
			mm.tab = t
			out.WriteString(ansiToHTML(mm.View()))
			out.WriteString("\n\n")
		}
		return out.String()
	}

	updated := "unknown"
	if !m.snap.At.IsZero() {
		updated = fmtZL(m.snap.At)
	}

	_, err := fmt.Fprintf(w, `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta http-equiv="refresh" content="%d">
<title>propscope — HF propagation</title>
<style>
  :root { color-scheme: dark; }
  body { margin:0; background:#0b0d12; color:#d8dee9;
         font:13px/1.25 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace; }
  header { padding:.75rem 1rem; border-bottom:1px solid #1c2029; }
  header b { color:#5aaeff; }
  header span { color:#6b7688; }
  main { padding:.5rem 1rem 2rem; }
  pre { margin:0; white-space:pre; font:inherit; }
  /* The frame is a fixed-width block: let it scroll rather than letting the
     page scroll sideways as a whole. */
  .frame { overflow-x:auto; }
  .narrow { display:none; }
  /* A terminal frame cannot reflow, so on a phone it scrolls -- but shrinking
     the type first cuts how far you have to swipe. */
  @media (max-width:820px) {
    .wide { display:none; } .narrow { display:block; font-size:9px; line-height:1.2; }
  }
  footer { padding:0 1rem 2rem; color:#4a5262; }
  a { color:#5aaeff; }
</style></head><body>
<header><b>propscope</b> <span>— HF propagation · Austin TX · updated %s · refreshes every %ds</span></header>
<main>
<div class="frame wide"><pre>%s</pre></div>
<div class="frame narrow"><pre>%s</pre></div>
</main>
<footer>Measured, not predicted. Sources: wspr.live, NOAA SWPC, prop.kc2g.com.
&nbsp;<a href="https://github.com/pkbatx/propscope">github.com/pkbatx/propscope</a></footer>
</body></html>
`, refreshSec, html.EscapeString(updated), refreshSec, render(wide), render(narrow))
	return err
}
