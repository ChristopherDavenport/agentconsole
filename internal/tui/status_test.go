package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client"
	"github.com/ChristopherDavenport/agentconsole/view"
)

func TestStatusTextShowsRunningCost(t *testing.T) {
	m := view.Model{
		Usage: openresponses.Usage{InputTokens: 300, OutputTokens: 50, TotalTokens: 350},
		UsageByModel: map[string]openresponses.Usage{
			"model": {InputTokens: 300, OutputTokens: 50, TotalTokens: 350},
		},
	}
	cost := client.Cost(func(model string, u openresponses.Usage) (float64, bool) {
		if model != "model" {
			return 0, false
		}
		return (float64(u.InputTokens) + 2*float64(u.OutputTokens)) / 1000, true
	})
	s := statusText(m, "", true, cost)
	for _, want := range []string{"tokens 300 in, 50 out", "$0.4000"} {
		if !strings.Contains(s, want) {
			t.Errorf("status lacks %q:\n%s", want, s)
		}
	}
}

func TestStatusTextNamesUnpricedModels(t *testing.T) {
	m := view.Model{
		Usage: openresponses.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		UsageByModel: map[string]openresponses.Usage{
			"provider/model-name": {InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		},
	}
	cost := client.Cost(func(string, openresponses.Usage) (float64, bool) { return 0, false })
	s := statusText(m, "", true, cost)
	if !strings.Contains(s, "unpriced: provider/model-name") {
		t.Errorf("status does not name the unpriced model:\n%s", s)
	}
	if strings.Contains(s, "$") {
		t.Errorf("status shows a cost for an unpriced model:\n%s", s)
	}
}

func TestRunFiguresAreTheCurrentTurns(t *testing.T) {
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	run := view.Run{ID: "r2", Started: t0, Usage: openresponses.Usage{InputTokens: 1_300_000, OutputTokens: 534_000}}
	going := view.Model{Run: run, Turn: view.Turn{State: view.Running, RunID: "r2"}}
	if got, want := runFigures(going, stateRunning, t0.Add(19*time.Second)), "(19s · 1.3M↑ / 534k↓)"; got != want {
		t.Errorf("running: %q, want %q", got, want)
	}
	if got, want := runFigures(going, stateAborting, t0.Add(65*time.Second)), "(1m05s · 1.3M↑ / 534k↓)"; got != want {
		t.Errorf("aborting: %q, want %q", got, want)
	}
	// A run whose start entry has not landed yet: the line's last run is
	// the one before it, and is not taken for it.
	ahead := going
	ahead.Turn.RunID = "r3"
	if got := runFigures(ahead, stateRunning, t0); got != "" {
		t.Errorf("the run before taken for the current one: %q", got)
	}
	// Waiting on a permission, the clock stops at the run's end.
	waiting := view.Model{Run: run, Turn: view.Turn{State: view.RequiresAction, RunID: "r2"}}
	waiting.Run.Ended = t0.Add(42 * time.Second)
	if got, want := runFigures(waiting, stateRequiresAction, t0.Add(time.Hour)), "(42s · 1.3M↑ / 534k↓)"; got != want {
		t.Errorf("requires action: %q, want %q", got, want)
	}
	if got := runFigures(waiting, stateIdle, t0); got != "" {
		t.Errorf("idle has no current turn: %q", got)
	}
	if got := runFigures(view.Model{Turn: view.Turn{State: view.Running}}, stateRunning, t0); got != "" {
		t.Errorf("no run on the line: %q", got)
	}
}

func TestCompactTokensKeepsThreeFigures(t *testing.T) {
	for n, want := range map[int]string{
		950: "950", 1000: "1.0k", 12_500: "12.5k", 99_949: "99.9k", 99_950: "100k",
		534_000: "534k", 999_499: "999k", 999_500: "1.0M", 1_300_000: "1.3M", 123_000_000: "123M",
	} {
		if got := compactTokens(n); got != want {
			t.Errorf("compactTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestElapsed(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Second: "0s", 0: "0s", 19*time.Second + 900*time.Millisecond: "19s",
		65 * time.Second: "1m05s", time.Hour + 2*time.Minute + 30*time.Second: "1h02m",
	} {
		if got := elapsed(d); got != want {
			t.Errorf("elapsed(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestTheRunLineIsOneLineAtAnyWidth(t *testing.T) {
	figures := "(19s · 1.3M↑ / 534k↓)"
	phase := "running mcp__github__search_code"
	for width, want := range map[int]string{
		80: "⠹ " + figures + " " + phase,
		40: "⠹ " + figures + " running mcp__git", // the phase cut to the room left
		30: "⠹ running mcp__github__search_",     // too little room: the figures go
		8:  "⠹ runnin",
	} {
		got := ansi.Strip(runLine(stateRunning, phase, figures, "⠹", width))
		if got != want || lipgloss.Width(got) > width {
			t.Errorf("at %d: %q, want %q", width, got, want)
		}
	}
	if got := ansi.Strip(runLine(stateRunning, "", "", "⠹", 40)); got != "⠹ running" {
		t.Errorf("with no phase or figures: %q", got)
	}
	if got := ansi.Strip(runLine(stateAborting, "writing", figures, "⠹", 40)); got != "⠹ "+figures+" aborting" {
		t.Errorf("aborting: %q", got)
	}
	if got := ansi.Strip(runLine(stateRequiresAction, "", figures, "", 40)); got != "◆ requires action "+figures {
		t.Errorf("requires action: %q", got)
	}
	if got := ansi.Strip(runLine(stateRequiresAction, "", figures, "", 30)); got != "◆ requires action" {
		t.Errorf("requires action, too narrow, the figures go first: %q", got)
	}
}

func TestTheRunPhaseIsWhatTheLoopIsDoing(t *testing.T) {
	call := func(name string, s view.CallState) view.Row {
		return view.Row{Item: &openresponses.FunctionCall{Name: name}, Call: &view.Call{Name: name, State: s}}
	}
	streaming := func(it openresponses.Item) view.Row { return view.Row{Live: true, Open: true, Item: it} }
	writing := streaming(&openresponses.Message{Role: openresponses.RoleAssistant})
	thinking := streaming(&openresponses.ReasoningItem{})
	ended := call("grep", view.CallEnded)
	for _, c := range []struct {
		name    string
		rows    []view.Row
		attempt int
		want    string
	}{
		{"nothing streamed yet", []view.Row{ended}, 0, "waiting"},
		{"a model call failed", nil, 1, "retrying"},
		{"reasoning streams", []view.Row{thinking}, 1, "thinking"},
		{"a message streams", []view.Row{thinking, writing}, 0, "writing"},
		{"a reasoning item that ended", []view.Row{{Live: true, Item: &openresponses.ReasoningItem{}}}, 0, "waiting"},
		{"a call's arguments stream", []view.Row{writing, call("read", view.CallOpen)}, 0, "calling read"},
		{"calls not run yet", []view.Row{call("read", view.CallOpen), call("grep", view.CallOpen)}, 0, "calling 2 tools"},
		{"a tool runs", []view.Row{ended, call("read", view.CallRunning), call("grep", view.CallOpen)}, 0, "running read"},
		{"tools run", []view.Row{call("read", view.CallRunning), call("grep", view.CallRunning)}, 0, "running 2 tools"},
		{"a deferred call", []view.Row{call("bash", view.CallDeferred)}, 0, "waiting"},
	} {
		m := view.Model{Rows: c.rows, Turn: view.Turn{State: view.Running, Attempt: c.attempt}}
		if got := runPhase(m); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestSessionTimeIsTheRunsThatEndedAndTheOneGoing(t *testing.T) {
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	m := view.Model{Worked: 2 * time.Minute, Run: view.Run{ID: "r2", Started: t0}, Turn: view.Turn{State: view.Running, RunID: "r2"}}
	if got, want := sessionTime(m, stateRunning, t0.Add(19*time.Second)), "2m19s"; got != want {
		t.Errorf("running: %q, want %q", got, want)
	}
	// Ended, the run is in Worked already and the clock is not added.
	idle := m
	idle.Turn.State, idle.Run.Ended = view.Idle, t0.Add(19*time.Second)
	if got, want := sessionTime(idle, stateIdle, t0.Add(time.Hour)), "2m00s"; got != want {
		t.Errorf("idle: %q, want %q", got, want)
	}
	if got := sessionTime(view.Model{}, stateIdle, t0); got != "" {
		t.Errorf("no run yet: %q", got)
	}
	if got := statusText(m, "2m19s", true, nil); !strings.HasPrefix(got, "time 2m19s") {
		t.Errorf("the status line does not lead with the session's time: %q", got)
	}
}
