package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ChristopherDavenport/agentconsole/client"
	"github.com/ChristopherDavenport/agentconsole/view"
)

// StatusConfig is the status line's make-up: where it sits, how it is
// drawn and which segments it shows, in order. The zero value is
// today's line: the default segments, plain, on the top row.
type StatusConfig struct {
	// Bottom puts the line on the screen's last row, under the input's
	// lower bar, and the conversation on the first; the notices then
	// take a row of their own just over the line, when there are any.
	Bottom bool
	// Powerline draws each segment as a colored block with a separator
	// glyph between blocks, instead of " | " on one reversed line.
	Powerline bool
	// Segments are the names shown, in order: built-ins
	// ([BuiltinSegments]) and Host's. Nil is [DefaultSegments].
	Segments []string
	// Host are the host's segments by name, read as the line is drawn.
	Host map[string]func() Segment
	// Colors are the segments' colors, by name.
	Colors map[string]Colors
	// Window is the context window of a model, in tokens, 0 when not
	// known; nil knows none.
	Window func(model string) int
	// Model is the model the host will run next, "" when it does not
	// say; nil says nothing.
	Model func() string
}

// Segment is a host segment's value: its text, "" to hide it with its
// separator, and its color, "" for the configured one.
type Segment struct {
	Text  string
	Color string
}

// Colors is a segment's color, and for a numeric one the colors past
// thresholds of its value.
type Colors struct {
	Color      string
	Thresholds []Threshold
}

// Threshold colors a numeric segment whose value is At or more, unless
// a higher threshold it also reaches says otherwise.
type Threshold struct {
	At    float64
	Color string
}

// StatusMsg asks for the status line to be drawn again: a host segment's
// value changed.
type StatusMsg struct{}

// WithStatus sets the status line's make-up.
func WithStatus(c StatusConfig) Option { return func(m *Model) { m.status = c } }

// The built-in segments, and whether each has a number its color's
// thresholds read: context in percent of the window, tokens in and out
// summed, cost in US dollars, time in seconds, turn and retry as counted.
var builtins = map[string]bool{
	"time": true, "tokens": true, "cost": true, "model": false, "context": true,
	"retry": true, "turn": true, "session": false, "verified": false, "stopped": false,
}

// BuiltinSegments are the names of the segments the client derives from
// the record and the events.
func BuiltinSegments() []string {
	return []string{"time", "tokens", "cost", "model", "context", "retry", "turn", "session", "verified", "stopped"}
}

// DefaultSegments are the segments of the line left unconfigured, in its
// order.
func DefaultSegments() []string {
	return []string{"time", "tokens", "cost", "model", "retry", "turn", "session", "verified", "stopped"}
}

// defaultContext colors the context as it fills: green, yellow from
// half, red from four fifths.
var defaultContext = []Threshold{{0, "2"}, {50, "3"}, {80, "1"}}

// Check refuses a make-up the line cannot draw: a segment, or a color
// for one, that is neither built in nor the host's, a host segment named
// as a built-in, or thresholds on a segment that has no number.
func (c StatusConfig) Check() error {
	var errs []error
	for name := range c.Host {
		if _, ok := builtins[name]; ok {
			errs = append(errs, fmt.Errorf("status: host segment %q has a built-in's name", name))
		}
	}
	for _, name := range c.Segments {
		_, builtin := builtins[name]
		if _, host := c.Host[name]; !builtin && !host {
			errs = append(errs, fmt.Errorf("status: unknown segment %q", name))
		}
	}
	for name, col := range c.Colors {
		_, builtin := builtins[name]
		if _, host := c.Host[name]; !builtin && !host {
			errs = append(errs, fmt.Errorf("status: color for unknown segment %q", name))
			continue
		}
		if len(col.Thresholds) > 0 && !builtins[name] {
			errs = append(errs, fmt.Errorf("status: segment %q has no number for thresholds", name))
		}
	}
	return errors.Join(errs...)
}

// piece is a segment as drawn: its name, text and color.
type piece struct {
	name, text, color string
}

// statusIn is what the built-in segments are read from.
type statusIn struct {
	v        view.Model
	worked   time.Duration
	working  bool // whether the line holds a run, so worked is shown
	verified bool
	cost     client.Cost
}

// pieces are the segments that show, in order, with their colors.
func (c StatusConfig) pieces(in statusIn) []piece {
	names := c.Segments
	if names == nil {
		names = DefaultSegments()
	}
	var ps []piece
	for _, name := range names {
		var p piece
		if fn, ok := c.Host[name]; ok {
			s := fn()
			p = piece{name: name, text: s.Text, color: s.Color}
			if p.color == "" {
				p.color = c.Colors[name].Color
			}
		} else {
			text, value, known := c.builtin(name, in)
			p = piece{name: name, text: text, color: c.color(name, value, known)}
		}
		if p.text != "" {
			ps = append(ps, p)
		}
	}
	return ps
}

// color is a built-in segment's color: the highest threshold its value
// reaches, when it has one, else the configured color.
func (c StatusConfig) color(name string, value float64, known bool) string {
	col, set := c.Colors[name]
	ts := col.Thresholds
	if !set && name == "context" {
		ts = defaultContext
	}
	out, best, found := col.Color, 0.0, false
	for _, t := range ts {
		if known && value >= t.At && (!found || t.At >= best) {
			out, best, found = t.Color, t.At, true
		}
	}
	return out
}

// builtin is a built-in segment's text, "" when it has nothing to say,
// and its number, when it has one.
func (c StatusConfig) builtin(name string, in statusIn) (string, float64, bool) {
	v := in.v
	u := v.Usage
	switch name {
	case "time":
		if !in.working {
			return "", 0, false
		}
		return "time " + elapsed(in.worked), in.worked.Seconds(), true
	case "tokens":
		if u.TotalTokens > 0 || u.InputTokens > 0 || u.OutputTokens > 0 {
			return fmt.Sprintf("tokens %s in, %s out", compactTokens(u.InputTokens), compactTokens(u.OutputTokens)), float64(u.InputTokens + u.OutputTokens), true
		}
	case "cost":
		if in.cost == nil || (u.TotalTokens == 0 && u.InputTokens == 0 && u.OutputTokens == 0) {
			return "", 0, false
		}
		p := view.Price(v.UsageByModel, in.cost)
		switch {
		case p.Priced:
			return fmt.Sprintf("$%.4f", p.Total), p.Total, true
		case len(p.Unpriced) > 0:
			return "unpriced: " + strings.Join(p.Unpriced, ", "), 0, false
		}
	case "model":
		return c.modelText(v), 0, false
	case "context":
		return c.contextText(v)
	case "retry":
		if v.Turn.Attempt > 0 {
			return fmt.Sprintf("retry %d", v.Turn.Attempt), float64(v.Turn.Attempt), true
		}
	case "turn":
		if v.Turn.Number > 0 {
			return fmt.Sprintf("turn %d", v.Turn.Number), float64(v.Turn.Number), true
		}
	case "session":
		if s := v.Session; s != "" {
			if len(s) > 12 {
				s = s[:12]
			}
			return "session " + s, 0, false
		}
	case "verified":
		if !in.verified {
			return "unverified", 0, false
		}
	case "stopped":
		switch {
		case v.Turn.Withheld:
			return "last reply withheld", 0, false
		case v.Turn.Cause != "":
			return "last run stopped: " + causeText(v.Turn.Cause), 0, false
		}
	}
	return "", 0, false
}

// recorded is the model the record and the events say: the turn's, or
// the config's in force.
func recorded(v view.Model) string {
	if v.Turn.Model != "" {
		return v.Turn.Model
	}
	return v.Config
}

// modelText names the model: the host's, when it names one, with the
// record's beside it while they differ (the record names the next run's
// model only once that run has started), else the record's.
func (c StatusConfig) modelText(v view.Model) string {
	rec, host := recorded(v), ""
	if c.Model != nil {
		host = c.Model()
	}
	switch {
	case host == "" && rec == "":
		return ""
	case host == "":
		return "model " + rec
	case rec != "" && rec != host:
		return "model " + host + " (record: " + rec + " until the next run)"
	}
	return "model " + host
}

// contextText is how full the context was at the last model call: its
// input tokens, of the window the host gives for the model, as
// "ctx 41% 405k/1M"; "ctx 405k" when the window is not known, and
// "-- ctx" before a call.
func (c StatusConfig) contextText(v view.Model) (string, float64, bool) {
	in := v.LastCall.Usage.InputTokens
	if in <= 0 {
		return "-- ctx", 0, false
	}
	model := ""
	if c.Model != nil {
		model = c.Model()
	}
	if model == "" {
		model = recorded(v)
	}
	if model == "" {
		model = v.LastCall.Model
	}
	window := 0
	if c.Window != nil {
		window = c.Window(model)
	}
	if window <= 0 {
		return "ctx " + compactTokens(in), 0, false
	}
	pct := float64(in) * 100 / float64(window)
	return fmt.Sprintf("ctx %.0f%% %s/%s", pct, compactTokens(in), compactTokens(window)), pct, true
}

// plainSep is what the plain line puts between two pieces: " | ", and
// ", " between the tokens and the cost right after them that prices
// them.
func plainSep(prev, p piece) string {
	if prev.name == "tokens" && p.name == "cost" {
		return ", "
	}
	return " | "
}

// The powerline look: a block's colors when its segment has none, the
// glyph between blocks, and the text on a colored block.
const (
	powerBlock = "8"
	powerText  = "15"
	powerInk   = "0"
	powerSep   = ""
)

// statusRows are the status line's rows: top, the first row, "" when the
// line is at the bottom, and bottom, the last rows, "" when the line is on
// top. Each row fills the width. On top the notices (the frozen banner, a
// stopped feed, the client's note) share the line's row; at the bottom
// they take a row of their own just over the line, while there are any,
// so the conversation's first row never moves as they come and go. A
// line wider than the terminal loses whole segments from the right, never
// wraps, and is cut only when the notices alone are too wide.
func (m *Model) statusRows(state runState, now time.Time) (top, bottom string) {
	c := m.status
	d, working := sessionWorked(m.view, state, now)
	ps := c.pieces(statusIn{v: m.view, worked: d, working: working, verified: m.verified, cost: m.cost})
	banner := m.frozenBanner()
	if !c.Bottom {
		var pre, post string
		if banner != "" {
			pre = banner + " | "
		}
		pre += feedNote(m.feedErr)
		if m.note != "" {
			post = " | " + m.note
		}
		return m.drawLine(pre, ps, post), ""
	}
	var notes []string
	for _, n := range []string{banner, strings.TrimSuffix(feedNote(m.feedErr), " | "), m.note} {
		if n != "" {
			notes = append(notes, n)
		}
	}
	line := m.drawLine("", ps, "")
	if len(notes) > 0 {
		return "", m.drawLine(strings.Join(notes, " | "), nil, "") + "\n" + line
	}
	return "", line
}

// vpTop is the screen row the viewport starts on: under the status line
// on top, the first row with the line at the bottom (its notices sit
// over it, below the input).
func (m *Model) vpTop() int {
	if m.status.Bottom {
		return 0
	}
	return 1
}

// statusRowsUsed is how many rows the status line takes: two for a line
// at the bottom with a notice over it, else one.
func (m *Model) statusRowsUsed() int {
	if !m.status.Bottom {
		return 1
	}
	if m.frozenBanner() != "" || m.feedErr != "" || m.note != "" {
		return 2
	}
	return 1
}

// drawLine draws one status row: pre, the pieces and post, cut to the
// width at a piece and filled to it.
func (m *Model) drawLine(pre string, ps []piece, post string) string {
	w := max(m.width, 1)
	draw := m.drawPlain
	if m.status.Powerline {
		draw = m.drawPowerline
	}
	for n := len(ps); ; n-- {
		s, width := draw(pre, ps[:n], post)
		if width <= w || n == 0 {
			if width > w {
				s, width = ansi.Truncate(s, w, ""), w
			}
			return s + statusFill(m.status.Powerline, w-width)
		}
	}
}

// statusFill fills the row past the line: reversed spaces on the plain
// line, the terminal's own past the last block.
func statusFill(powerline bool, n int) string {
	if n <= 0 {
		return ""
	}
	if powerline {
		return strings.Repeat(" ", n)
	}
	return statusStyle.Render(strings.Repeat(" ", n))
}

// drawPlain is the plain line: reversed, " | " between the pieces, a
// piece with a color drawn in it.
func (m *Model) drawPlain(pre string, ps []piece, post string) (string, int) {
	var b strings.Builder
	width := 0
	put := func(s, color string) {
		if s == "" {
			return
		}
		st := statusStyle
		if color != "" {
			st = st.Foreground(lipgloss.Color(color))
		}
		b.WriteString(st.Render(s))
		width += lipgloss.Width(s)
	}
	put(pre, "")
	for i, p := range ps {
		if i > 0 {
			put(plainSep(ps[i-1], p), "")
		}
		put(p.text, p.color)
	}
	put(post, "")
	return b.String(), width
}

// drawPowerline is the powerline line: each piece a block, its color
// the block's, with the glyph between blocks in the color of the one it
// leaves on the one it enters. The notices around them are plain.
func (m *Model) drawPowerline(pre string, ps []piece, post string) (string, int) {
	var b strings.Builder
	width := 0
	if pre != "" {
		b.WriteString(statusStyle.Render(pre))
		width += lipgloss.Width(pre)
	}
	block := func(p piece) (bg, fg string) {
		if p.color == "" {
			return powerBlock, powerText
		}
		return p.color, powerInk
	}
	for i, p := range ps {
		bg, fg := block(p)
		text := " " + p.text + " "
		b.WriteString(lipgloss.NewStyle().Background(lipgloss.Color(bg)).Foreground(lipgloss.Color(fg)).Render(text))
		sep := lipgloss.NewStyle().Foreground(lipgloss.Color(bg))
		if i+1 < len(ps) {
			next, _ := block(ps[i+1])
			sep = sep.Background(lipgloss.Color(next))
		}
		b.WriteString(sep.Render(powerSep))
		width += lipgloss.Width(text) + lipgloss.Width(powerSep)
	}
	if post != "" {
		b.WriteString(statusStyle.Render(post))
		width += lipgloss.Width(post)
	}
	return b.String(), width
}

// modeKind names the next-mode intent: Shift-Tab asks the host for its
// next mode, which it shows in a segment of its own.
const modeKind = "mode"

// modeDone shows a refusal of the next-mode intent on the status line,
// where the mode is, as the client's note; a mode taken clears an
// earlier refusal.
func (m *Model) modeDone(err error) tea.Cmd {
	switch {
	case err != nil:
		m.setNote(modeKind + ": " + err.Error())
	case strings.HasPrefix(m.note, modeKind+": "):
		m.setNote("")
	}
	return nil
}
