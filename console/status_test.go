package console_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/ChristopherDavenport/agentconsole/console"
	"github.com/ChristopherDavenport/agentconsole/internal/scripted"
)

// hostSegs are host segments whose values a test sets, as a host's
// state would change under them.
type hostSegs struct {
	mu   sync.Mutex
	vals map[string]console.Segment
}

func (h *hostSegs) set(name, text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.vals[name] = console.Segment{Text: text}
}

func (h *hostSegs) segments(names ...string) map[string]func() console.Segment {
	out := map[string]func() console.Segment{}
	for _, n := range names {
		out[n] = func() console.Segment {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.vals[n]
		}
	}
	return out
}

// rows are the screen's rows.
func (r *rig) rows() []string { return strings.Split(r.out.Screen(), "\n") }

// TestTheStatusLineSitsWhereItIsPut: on top, the line is the screen's
// first row; at the bottom, its last, under the input's lower bar, and
// the top row goes to the conversation.
func TestTheStatusLineSitsWhereItIsPut(t *testing.T) {
	for _, pos := range []console.StatusPosition{console.StatusTop, console.StatusBottom} {
		t.Run(string(pos), func(t *testing.T) {
			h := &hostSegs{vals: map[string]console.Segment{"where": {Text: "HOST SEGMENT"}}}
			r := start(t, scripted.New(scripted.Say("pong")), console.WithStatus(console.StatusLine{
				Position: pos,
				Segments: []string{"where", "verified"},
				Host:     h.segments("where"),
			}))
			r.waitScreen("HOST SEGMENT")
			rows := r.rows()
			for len(rows) > 1 && strings.TrimSpace(rows[len(rows)-1]) == "" {
				rows = rows[:len(rows)-1]
			}
			first, last := rows[0], rows[len(rows)-1]
			on, off := first, last
			if pos == console.StatusBottom {
				on, off = last, first
			}
			if !strings.Contains(on, "HOST SEGMENT") || strings.Contains(off, "HOST SEGMENT") {
				t.Fatalf("the line is not on the %s row:\n%s", pos, r.out.Screen())
			}
			if pos == console.StatusBottom && !strings.Contains(rows[len(rows)-2], "───") {
				t.Errorf("the line is not under the input's lower bar:\n%s", r.out.Screen())
			}
		})
	}
}

// TestAHostSegmentRedrawsWithoutAKey: a host segment's change sent on
// Updates is drawn with no key pressed, its order is the option's, and
// a segment whose text is "" leaves with its separator.
func TestAHostSegmentRedrawsWithoutAKey(t *testing.T) {
	h := &hostSegs{vals: map[string]console.Segment{"a": {Text: "alpha"}, "b": {Text: "beta"}, "c": {Text: "gamma"}}}
	updates := make(chan struct{}, 1)
	r := start(t, scripted.New(scripted.Say("pong")), console.WithStatus(console.StatusLine{
		Segments: []string{"c", "a", "b"},
		Host:     h.segments("a", "b", "c"),
		Updates:  updates,
	}))
	r.waitScreen("gamma | alpha | beta")
	h.set("a", "")
	updates <- struct{}{}
	r.waitScreen("gamma | beta")
	h.set("b", "BETA NOW")
	updates <- struct{}{}
	r.waitScreen("gamma | BETA NOW")
}

// TestAStatusLineItCannotDrawIsRefusedAtRun: a segment that is neither
// built in nor the host's, an unknown position or style, refuses Run
// before the program starts.
func TestAStatusLineItCannotDrawIsRefusedAtRun(t *testing.T) {
	for name, s := range map[string]console.StatusLine{
		"unknown segment": {Segments: []string{"model", "weather"}},
		"position":        {Position: "middle"},
		"style":           {Style: "fancy"},
		"host built-in":   {Host: map[string]func() console.Segment{"model": func() console.Segment { return console.Segment{} }}},
		"thresholds":      {Segments: []string{"session"}, Colors: map[string]console.SegmentColor{"session": {Thresholds: []console.Threshold{{At: 1, Color: "1"}}}}},
	} {
		err := console.Run(context.Background(), nil, nil, nil, console.WithStatus(s))
		if err == nil {
			t.Errorf("%s: Run took %+v", name, s)
		}
	}
}
