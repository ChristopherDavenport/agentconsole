package tui_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client"
	"github.com/ChristopherDavenport/agentconsole/internal/tui"
)

// recordedUnder is a session whose one run was made under model, as a
// session the host opens after a relaunch is.
func recordedUnder(model string) starter {
	return func(ctx context.Context, store agentsession.Store) (*session.Recorder, error) {
		rec, _, err := session.Start(ctx, store, agentsession.Header{Records: agentsession.AllRecords})
		if err != nil {
			return nil, err
		}
		cfg := cfgWith(say(nil, nil, "answered under "+model))
		cfg.ModelName = model
		ag := agentturn.New(cfg)
		detach := rec.Attach(ag)
		defer detach()
		if _, err := ag.Prompt(ctx, openresponses.UserText("before the relaunch")); err != nil {
			return nil, err
		}
		return rec, nil
	}
}

// The model the host configured shows before the first run after a
// relaunch, with the record's beside it until a run under the host's
// lands; then only the host's (#7).
func TestTheHostsModelShowsBeforeTheFirstRun(t *testing.T) {
	cfg := cfgWith(say(nil, nil, "answered under b"))
	cfg.ModelName = "model-b"
	a := newAppWith(t, cfg, recordedUnder("model-a"), nil, tui.WithStatus(tui.StatusConfig{
		Model: func() string { return "model-b" },
	}))
	a.waitFor("the host's model with the record's", has("answered under model-a", "model model-b (record: model-a until the next run)"))
	a.submit("after the relaunch")
	a.waitFor("the host's model alone", all(has("answered under b", "model model-b", "idle"), lacks("record:")))
}

// statusRow is the screen's first row, where the status line is.
func statusRow(screen string) string { first, _, _ := strings.Cut(screen, "\n"); return first }

// Shift-Tab sends the next-mode intent, which names no mode; the host
// that cycles its modes shows the new one in its own segment, in the
// colour it gives it (#43).
func TestShiftTabAsksTheHostForTheNextMode(t *testing.T) {
	var mu sync.Mutex
	modes, at := []string{"default", "plan", "bypass"}, 0
	ctl := &intents{}
	act := func(ctx context.Context, in client.Intent) error {
		if _, ok := in.(client.NextMode); ok {
			mu.Lock()
			at = (at + 1) % len(modes)
			mu.Unlock()
		}
		return ctl.act(ctx, in)
	}
	mode := func() tui.Segment {
		mu.Lock()
		defer mu.Unlock()
		s := tui.Segment{Text: "mode " + modes[at]}
		if modes[at] == "bypass" {
			s.Colour = "1" // a mode that asks nothing is a warning
		}
		return s
	}
	a := newViewApp(t, &handSource{}, act, tui.WithStatus(tui.StatusConfig{
		Segments: []string{"mode", "verified"},
		Host:     map[string]func() tui.Segment{"mode": mode},
	}))
	a.waitFor("the first mode", func(s string) bool { return strings.Contains(statusRow(s), "mode default") })
	a.key("shift+tab")
	if _, ok := ctl.next(t, 1).(client.NextMode); !ok {
		t.Fatalf("intent = %#v, want the next mode", ctl.got[0])
	}
	a.waitFor("the next mode", func(s string) bool { return strings.Contains(statusRow(s), "mode plan") })
	a.key("shift+tab")
	ctl.next(t, 2)
	a.waitFor("the mode after", func(s string) bool { return strings.Contains(statusRow(s), "mode bypass") })
}

// With Direct, a bare control has no modes: the next-mode intent is
// refused, and the refusal shows on the status line (#43).
func TestDirectRefusesTheNextModeOnTheStatusLine(t *testing.T) {
	a := newApp(t, cfgWith())
	a.key("shift+tab")
	a.waitFor("the refusal on the status line", func(s string) bool {
		return strings.Contains(statusRow(s), "mode: "+client.ErrNoModes.Error())
	})
}
