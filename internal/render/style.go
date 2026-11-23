package render

import (
	"io"

	"github.com/charmbracelet/lipgloss"

	"github.com/amberpixels/kyo/internal/events"
)

// Styles holds the lipgloss styles output is colored with. Built over the
// writer the output goes to, it degrades by itself: a non-terminal or a dumb
// terminal yields plain text, so callers never branch on color.
type Styles struct {
	title   lipgloss.Style // header and summary section titles
	ok      lipgloss.Style
	changed lipgloss.Style
	failed  lipgloss.Style
	skipped lipgloss.Style
	muted   lipgloss.Style // nonzero skipped, ignored and rescued counters
	spin    lipgloss.Style // the running play's glyph
	bar     lipgloss.Style // a progress bar's filled cells
	track   lipgloss.Style // a progress bar's empty cells
	pct     lipgloss.Style // a progress bar's percentage
	done    lipgloss.Style // a finished play's check mark
	took    lipgloss.Style // a finished play's duration
	dim     lipgloss.Style // secondary text: pending hosts, log path, notes
	warn    lipgloss.Style // the warnings count and section
	timer   lipgloss.Style // clocks
	success lipgloss.Style // a summary headline with nothing failed
	added   lipgloss.Style // diff lines
	removed lipgloss.Style
	hunk    lipgloss.Style
}

// The palette matches ele's, so the two tools read alike side by side.
var (
	colTeal  = lipgloss.Color("#14B8A6")
	colGreen = lipgloss.Color("#22C55E")
	colRed   = lipgloss.Color("#EF4444")
	colAmber = lipgloss.Color("#F59E0B")
	colPink  = lipgloss.Color("#F472B6")
	colGray  = lipgloss.Color("#6B7280")
)

// NewStyles builds styles for w, detecting its color profile. A NO_COLOR in
// env yields plain styles, because env may differ from the process's own.
func NewStyles(w io.Writer, env []string) *Styles {
	if Getenv(env, "NO_COLOR") != "" {
		return plainStyles()
	}
	return newStyles(lipgloss.NewRenderer(w))
}

// newStyles builds the colored styles over r, whose profile decides how much
// of the color survives.
func newStyles(r *lipgloss.Renderer) *Styles {
	s := r.NewStyle
	return &Styles{
		title:   s().Bold(true),
		ok:      s().Foreground(colGreen),
		changed: s().Foreground(colAmber),
		failed:  s().Foreground(colRed).Bold(true),
		skipped: s().Foreground(colGray),
		muted:   s().Foreground(colGray),
		spin:    s().Foreground(colTeal),
		bar:     s().Foreground(colTeal),
		track:   s().Foreground(colGray).Faint(true),
		pct:     s().Foreground(colTeal),
		done:    s().Foreground(colGreen).Bold(true),
		took:    s().Foreground(colGreen).Faint(true),
		dim:     s().Faint(true),
		warn:    s().Foreground(colAmber),
		timer:   s().Foreground(colPink),
		success: s().Foreground(colGreen).Bold(true),
		added:   s().Foreground(colGreen),
		removed: s().Foreground(colRed),
		hunk:    s().Foreground(colTeal),
	}
}

// plainStyles renders everything unstyled: zero-value lipgloss styles return
// their input verbatim. Used for tests and when no Styles are given.
func plainStyles() *Styles { return &Styles{} }

// status is the glyph and style for a host's result.
func (st *Styles) status(s events.Status) string {
	switch s {
	case events.StatusOK:
		return st.ok.Render("✓")
	case events.StatusChanged:
		return st.changed.Render("~")
	case events.StatusSkipped:
		return st.skipped.Render("-")
	case events.StatusFailed:
		return st.failed.Render("✗")
	case events.StatusIgnored:
		return st.dim.Render("✗")
	case events.StatusUnreachable:
		return st.failed.Render("!")
	}
	return "?"
}
