package console

import (
	"fmt"

	"github.com/ChristopherDavenport/agentconsole/internal/tui"
)

// StatusPosition is where the status line sits.
type StatusPosition string

// The status line's positions.
const (
	// StatusTop is the screen's first row, the default.
	StatusTop StatusPosition = "top"
	// StatusBottom is the screen's last row, under the input's lower
	// bar, and the conversation starts on the first row. The notices (a
	// read-only view's banner, a stopped feed, what the last client
	// action did) take a row of their own just over the line, while
	// there are any, so the conversation does not move as they come and
	// go.
	StatusBottom StatusPosition = "bottom"
)

// StatusStyle is how the status line is drawn.
type StatusStyle string

// The status line's styles.
const (
	// StatusPlain is one reversed line with " | " between the segments,
	// the default.
	StatusPlain StatusStyle = "plain"
	// StatusPowerline draws each segment as a block in its colour, with
	// a powerline separator glyph (U+E0B0, which needs a font that has
	// it) between the blocks.
	StatusPowerline StatusStyle = "powerline"
)

// Segment is a host segment's value as the line is drawn: its text, ""
// to hide it along with its separator, and its colour, "" for the one
// [StatusLine.Colours] gives it.
//
// A colour is what lipgloss.Color takes: an ANSI colour by number ("1"
// red, "2" green, "3" yellow, "0" to "15" the basic colours, up to
// "255"), or "#rrggbb". On the plain line it colours the segment's text,
// which the reversed line shows as its block; on the powerline line it
// is the block's.
type Segment struct {
	Text   string
	Colour string
}

// SegmentColour is a segment's colour, and for a segment with a number
// the colours its value takes past thresholds.
type SegmentColour struct {
	Colour string
	// Thresholds colour a numeric built-in segment by its value: the
	// highest threshold it reaches. Only the segments with a number take
	// them: context (percent of the window), tokens (in and out
	// summed), cost (US dollars), time (seconds), turn and retry.
	Thresholds []Threshold
}

// Threshold is a colour a numeric segment takes at a value of At or
// more.
type Threshold struct {
	At     float64
	Colour string
}

// StatusLine is the status line's make-up. Its zero value is the line
// without [WithStatus]: the default segments, plain, on top.
type StatusLine struct {
	// Position is where the line sits; "" is [StatusTop].
	Position StatusPosition
	// Style is how it is drawn; "" is [StatusPlain].
	Style StatusStyle
	// Segments are the names shown, in order. A built-in
	// ([BuiltinSegments]) is derived from the record and the events:
	//
	//   - time: the session's time working, "time 4m05s"
	//   - tokens: its token use, "tokens 1.3M in, 534k out"
	//   - cost: its cost, "$0.4000", or the models [WithCost] has no
	//     price for (on the plain line, joined to tokens right before it
	//     with ", ")
	//   - model: the model, "model X": [StatusLine.Model]'s when it
	//     names one, with the record's beside it while they differ,
	//     "model B (record: A until the next run)"
	//   - context: how full the context was at the last model call, of
	//     the window [StatusLine.Window] gives, "ctx 41% 405k/1M", "-- ctx"
	//     before a call; green, yellow from 50% and red from 80% unless
	//     its colour is configured
	//   - retry, turn: the attempt being retried, the turn of the run
	//   - session: the session's ID, cut to 12 characters
	//   - verified: "unverified" for a record that is not the agent's own
	//   - stopped: what stopped the last run
	//
	// Any other name is one of Host's. Nil is [DefaultStatusSegments].
	// A segment with nothing to say is left out, separator and all. Run
	// refuses a name that is neither.
	Segments []string
	// Host are the host's segments by name: its executor, its mode, its
	// git branch. Each is called as the line is drawn, on the program's
	// goroutine, and must return promptly and be safe to call while the
	// host changes what it returns. A name may not be a built-in's.
	Host map[string]func() Segment
	// Colours are the segments' colours, by name. A host segment's own
	// colour comes first.
	Colours map[string]SegmentColour
	// Window is the context window of a model, in tokens, 0 when the
	// host does not know it, for the context segment; nil knows none,
	// and the segment shows the tokens alone.
	Window func(model string) int
	// Model is the model the host runs next, "" when it does not say.
	// The record names a run's model only once the run has started, so
	// after a relaunch or a switch the record's is the last run's; the
	// model segment names the host's, and the record's beside it until
	// they agree. It is called as Host's segments are.
	Model func() string
	// Updates redraws the line each time a value is received: the host
	// sends one after a host segment's value (or Model's) changes. Run
	// reads it until the program ends; a send that would block can be
	// dropped when one is already waiting (a channel with a buffer of
	// one, sent to without blocking), since one redraw reads every
	// segment.
	Updates <-chan struct{}
}

// BuiltinSegments are the names of the segments the client derives from
// the record and the events, as [StatusLine.Segments] lists them.
func BuiltinSegments() []string { return tui.BuiltinSegments() }

// DefaultStatusSegments are the segments of the line without
// [WithStatus], in its order: time, tokens, cost, model, retry, turn,
// session, verified, stopped.
func DefaultStatusSegments() []string { return tui.DefaultSegments() }

// WithStatus makes up the status line: where it sits, how it is drawn,
// which segments it shows and in what colours, the host's own segments,
// and what the context and model segments read from the host. Without
// it the line is the default segments, plain, on top. Run refuses a
// make-up it cannot draw.
func WithStatus(s StatusLine) Option {
	return func(c *config) { c.status, c.statusSet = s, true }
}

// statusConfig is s as the program takes it, or why it cannot be drawn.
func statusConfig(s StatusLine) (tui.StatusConfig, error) {
	c := tui.StatusConfig{
		Segments: s.Segments,
		Host:     map[string]func() tui.Segment{},
		Colours:  map[string]tui.Colours{},
		Window:   s.Window,
		Model:    s.Model,
	}
	switch s.Position {
	case "", StatusTop:
	case StatusBottom:
		c.Bottom = true
	default:
		return c, fmt.Errorf("console: status line position %q is not top or bottom", s.Position)
	}
	switch s.Style {
	case "", StatusPlain:
	case StatusPowerline:
		c.Powerline = true
	default:
		return c, fmt.Errorf("console: status line style %q is not plain or powerline", s.Style)
	}
	for name, fn := range s.Host {
		if fn == nil {
			return c, fmt.Errorf("console: host segment %q has no function", name)
		}
		c.Host[name] = func() tui.Segment { return tui.Segment(fn()) }
	}
	for name, col := range s.Colours {
		ts := make([]tui.Threshold, len(col.Thresholds))
		for i, t := range col.Thresholds {
			ts[i] = tui.Threshold(t)
		}
		c.Colours[name] = tui.Colours{Colour: col.Colour, Thresholds: ts}
	}
	if err := c.Check(); err != nil {
		return c, fmt.Errorf("console: %w", err)
	}
	return c, nil
}
