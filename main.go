// propscope -- HF propagation at a glance, from measurements this box already
// collects.
//
// Reads the collector's postgres directly rather than going through the
// collector's HTTP API. That is deliberate: this is an interactive tool run by
// one person on the host (the same posture as lazydocker talking to the docker
// socket), the data is already in postgres, and reading it directly means the
// display can be iterated on without rebuilding the collector image.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
	_ "time/tzdata" // embed the tz database so PROPSCOPE_TZ works in a scratch container

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mattn/go-isatty"
	"github.com/muesli/termenv"
)

const refreshEvery = 30 * time.Second

type tab int

const (
	tabHome tab = iota
	tabBands
	tabWaterfall
	tabSolar
	tabIono
	numTabs
)

var tabNames = [numTabs]string{"HOME", "BANDS", "WATERFALL", "SOLAR", "IONOSPHERE"}

type snapshotMsg Snapshot
type tickMsg time.Time

type model struct {
	pool *pgxpool.Pool
	snap Snapshot

	tab          tab
	w, h         int
	ready        bool
	loading      bool
	lastErr      error
	showHelp     bool
	selectedBand int
}

func initialModel(pool *pgxpool.Pool) model {
	return model{pool: pool, loading: true, w: 80, h: 24}
}

func (m model) load() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return snapshotMsg(Load(ctx, m.pool))
	}
}

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.load(), tick())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.ready = true
		return m, nil

	case tickMsg:
		return m, tea.Batch(m.load(), tick())

	case snapshotMsg:
		m.snap = Snapshot(msg)
		m.loading = false
		m.lastErr = m.snap.Err
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "r":
			m.loading = true
			return m, m.load()
		case "?":
			m.showHelp = !m.showHelp
			return m, nil
		case "tab", "l", "right":
			m.tab = (m.tab + 1) % numTabs
			return m, nil
		case "shift+tab", "h", "left":
			m.tab = (m.tab + numTabs - 1) % numTabs
			return m, nil
		case "1":
			m.tab = tabHome
		case "2":
			m.tab = tabBands
		case "3":
			m.tab = tabWaterfall
		case "4":
			m.tab = tabSolar
		case "5":
			m.tab = tabIono
		case "up", "k":
			if m.selectedBand > 0 {
				m.selectedBand--
			}
		case "down", "j":
			if m.selectedBand < len(Bands)-1 {
				m.selectedBand++
			}
		}
	}
	return m, nil
}

func (m model) View() string {
	if !m.ready {
		return "starting propscope..."
	}
	if m.showHelp {
		return m.viewHelp()
	}

	var body string
	switch m.tab {
	case tabHome:
		body = m.viewHome()
	case tabBands:
		body = m.viewBands()
	case tabWaterfall:
		body = m.viewWaterfall()
	case tabSolar:
		body = m.viewSolar()
	case tabIono:
		body = m.viewIono()
	}
	return m.chrome(body)
}

// ensureColor works around termenv mis-detecting this box as monochrome.
//
// termenv decides colour support by string-matching TERM for "256color" and by
// reading COLORTERM. Ghostty sets TERM=xterm-ghostty, which matches neither,
// and COLORTERM does not survive the SSH hop. termenv does not consult
// terminfo, so the local xterm-ghostty entry (a stub aliasing xterm-256color --
// see CLAUDE.md) never gets a vote and the profile lands on Ascii. lipgloss
// then silently strips every colour and the whole UI renders monochrome.
//
// So: if we are attached to a real terminal that is not "dumb", assume at least
// 256 colours, which has been a safe assumption for any xterm-alike for two
// decades. NO_COLOR still wins, per the convention.
func ensureColor() {
	if os.Getenv("NO_COLOR") != "" {
		return
	}
	if t := os.Getenv("TERM"); t == "" || t == "dumb" {
		return
	}
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		return
	}
	// NOTE the direction of this comparison. termenv orders its Profile
	// constants MOST capable first -- TrueColor=0, ANSI256=1, ANSI=2, Ascii=3 --
	// so "less capable than 256 colour" is `>`, not `<`. Getting this backwards
	// compiles, runs, and silently never upgrades anything.
	if lipgloss.ColorProfile() > wantProfile() {
		lipgloss.SetColorProfile(wantProfile())
	}
}

// wantProfile is the colour depth to force when termenv has under-detected.
//
// The gradients here are defined in 24-bit and quantised by lipgloss, so
// truecolor is a visible upgrade rather than a nicety: the waterfall goes from
// 20 discrete steps to a continuous ramp. Every terminal below advertises
// truecolor and has for years; the trouble is only that COLORTERM does not
// survive SSH, so nothing downstream can tell.
//
// PROPSCOPE_COLOR overrides: truecolor | 256 | ansi.
func wantProfile() termenv.Profile {
	switch strings.ToLower(os.Getenv("PROPSCOPE_COLOR")) {
	case "truecolor", "24bit":
		return termenv.TrueColor
	case "256", "ansi256":
		return termenv.ANSI256
	case "ansi", "16":
		return termenv.ANSI
	}
	if ct := strings.ToLower(os.Getenv("COLORTERM")); ct == "truecolor" || ct == "24bit" {
		return termenv.TrueColor
	}
	term := strings.ToLower(os.Getenv("TERM"))
	for _, known := range []string{"ghostty", "kitty", "wezterm", "alacritty",
		"iterm", "contour", "foot", "rio"} {
		if strings.Contains(term, known) {
			return termenv.TrueColor
		}
	}
	// Unknown terminal: 256 colours is the safe floor. Guessing truecolor here
	// would render as garbage on anything that genuinely lacks it.
	return termenv.ANSI256
}

func main() {
	// -dump renders every tab once to stdout and exits. It exists so the
	// display can be checked without a terminal -- and so the same numbers can
	// be piped somewhere else -- but it is also the honest way to verify a TUI
	// in a script, where driving the real event loop is unreliable.
	dump := flag.Bool("dump", false, "render all tabs once to stdout and exit")
	width := flag.Int("width", 120, "render width for -dump")
	height := flag.Int("height", 46, "render height for -dump")
	plain := flag.Bool("plain", false, "-dump without colour escapes")
	toHTML := flag.Bool("html", false, "render a standalone HTML page to stdout and exit")
	narrow := flag.Int("narrow", 96, "second, phone-sized render width for -html")
	refresh := flag.Int("refresh", 300, "meta-refresh interval in seconds for -html")
	flag.Parse()

	ensureColor()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool, err := Connect(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "propscope: cannot connect to postgres: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "propscope: postgres unreachable: %v\n"+
			"  expected database %s on %s:%s as user %s\n",
			err, getenv("PGDATABASE", "propscope"), getenv("PGHOST", "127.0.0.1"),
			getenv("PGPORT", "5432"), getenv("PGUSER", "propscope"))
		os.Exit(1)
	}

	if *toHTML {
		// Force truecolor regardless of where stdout is pointing: the target is
		// a browser, not this terminal, so the usual "is it a TTY" reasoning
		// does not apply and quantising to 256 would throw away colour the page
		// can display perfectly well.
		lipgloss.SetColorProfile(termenv.TrueColor)
		m := initialModel(pool)
		m.ready, m.loading = true, false
		m.snap = Load(ctx, pool)
		if m.snap.Err != nil {
			fmt.Fprintf(os.Stderr, "propscope: %v\n", m.snap.Err)
			os.Exit(1)
		}
		if err := WriteHTML(os.Stdout, m, *width, *narrow, *height, *refresh); err != nil {
			fmt.Fprintf(os.Stderr, "propscope: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *dump {
		// lipgloss asks termenv what the OUTPUT supports, and termenv correctly
		// answers "no colour" when stdout is a pipe or a file. That makes a
		// piped -dump monochrome, which defeats the point when the reason for
		// dumping is to look at it (`propscope -dump | less -R`) or to check the
		// palette. Force 256 colours here; -plain opts back out for grep.
		if !*plain {
			lipgloss.SetColorProfile(wantProfile())
		}
		m := initialModel(pool)
		m.w, m.h, m.ready, m.loading = *width, *height, true, false
		m.snap = Load(ctx, pool)
		if m.snap.Err != nil {
			fmt.Fprintf(os.Stderr, "propscope: %v\n", m.snap.Err)
			os.Exit(1)
		}
		for t := tab(0); t < numTabs; t++ {
			m.tab = t
			fmt.Println(m.View())
			fmt.Println()
		}
		return
	}

	p := tea.NewProgram(initialModel(pool), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "propscope: %v\n", err)
		os.Exit(1)
	}
}
