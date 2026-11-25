package tui

import (
	"image/color"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client"
	"github.com/ChristopherDavenport/agentconsole/internal/termtest"
	"github.com/ChristopherDavenport/agentconsole/view"
)

// drawnLine is the status line the program draws for v, left
// unconfigured, at now in state: the top row, its styles stripped and
// its fill trimmed.
func drawnLine(v view.Model, state runState, now time.Time, verified bool, cost client.Cost) string {
	m := &Model{view: v, verified: verified, cost: cost, width: 1000}
	top, bottom := m.statusRows(state, now)
	if bottom != "" {
		panic("the unconfigured line is at the bottom")
	}
	return strings.TrimRight(ansi.Strip(top), " ")
}

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
	s := drawnLine(m, stateIdle, time.Now(), true, cost)
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
	s := drawnLine(m, stateIdle, time.Now(), true, cost)
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
	if got := drawnLine(m, stateRunning, t0.Add(19*time.Second), true, nil); !strings.HasPrefix(got, "time 2m19s") {
		t.Errorf("the status line does not lead with the session's time: %q", got)
	}
}

// TestAWideStatusLineIsCutAtASegment: a line wider than the terminal
// loses whole segments from the right, plain or powerline, and is never
// wider than the row.
func TestAWideStatusLineIsCutAtASegment(t *testing.T) {
	ps := []piece{{name: "a", text: strings.Repeat("a", 20)}, {name: "b", text: strings.Repeat("b", 15)}, {name: "c", text: "ccc"}}
	for _, power := range []bool{false, true} {
		m := &Model{width: 40, status: StatusConfig{Powerline: power}}
		got := ansi.Strip(m.drawLine("", ps, ""))
		if lipgloss.Width(got) != 40 {
			t.Errorf("powerline %v: the row is %d wide, want 40: %q", power, lipgloss.Width(got), got)
		}
		if strings.Contains(got, "c") || !strings.Contains(got, strings.Repeat("a", 20)) {
			t.Errorf("powerline %v: not cut at a segment: %q", power, got)
		}
		wantB := !power // " a | b" fits 38 columns; the blocks' padding and glyphs do not
		if strings.Contains(got, strings.Repeat("b", 15)) != wantB {
			t.Errorf("powerline %v: b shown %v, want %v: %q", power, !wantB, wantB, got)
		}
		if strings.Contains(got, "b") && !strings.Contains(got, strings.Repeat("b", 15)) {
			t.Errorf("powerline %v: a segment cut in its middle: %q", power, got)
		}
	}
	// Notices alone too wide are cut to the row.
	m := &Model{width: 10}
	if got := ansi.Strip(m.drawLine(strings.Repeat("n", 30), ps, "")); lipgloss.Width(got) != 10 {
		t.Errorf("notices cut to %d columns: %q", lipgloss.Width(got), got)
	}
}

// TestTheContextSegmentColorsAsItFills: the context is the last call's
// input of the host's window for the model, green, yellow from half and
// red from four fifths, "-- ctx" before a call; a configured color or
// thresholds replace the default.
func TestTheContextSegmentColorsAsItFills(t *testing.T) {
	window := func(model string) int {
		if model == "big" {
			return 1_000_000
		}
		return 0
	}
	c := StatusConfig{Segments: []string{"context"}, Window: window}
	at := func(in int, model string) view.Model {
		return view.Model{Config: model, LastCall: view.ModelCall{Model: model, Usage: openresponses.Usage{InputTokens: in}}}
	}
	for _, tc := range []struct {
		v     view.Model
		text  string
		color string
	}{
		{view.Model{Config: "big"}, "-- ctx", ""},
		{at(410_000, "big"), "ctx 41% 410k/1.0M", "2"},
		{at(500_000, "big"), "ctx 50% 500k/1.0M", "3"},
		{at(850_000, "big"), "ctx 85% 850k/1.0M", "1"},
		{at(850_000, "small"), "ctx 850k", ""}, // no window known
	} {
		ps := c.pieces(statusIn{v: tc.v, verified: true})
		if len(ps) != 1 || ps[0].text != tc.text || ps[0].color != tc.color {
			t.Errorf("%+v: got %+v, want %q in %q", tc.v.LastCall, ps, tc.text, tc.color)
		}
	}
	c.Colors = map[string]Colors{"context": {Color: "4", Thresholds: []Threshold{{90, "5"}}}}
	if ps := c.pieces(statusIn{v: at(850_000, "big")}); ps[0].color != "4" {
		t.Errorf("below the configured threshold: %+v, want color 4", ps)
	}
	if ps := c.pieces(statusIn{v: at(950_000, "big")}); ps[0].color != "5" {
		t.Errorf("past the configured threshold: %+v, want color 5", ps)
	}
}

// TestTheUnconfiguredLineIsTodays: the default make-up draws what the
// line always said, in its order and joins.
func TestTheUnconfiguredLineIsTodays(t *testing.T) {
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	m := view.Model{
		Session: "0123456789abcdef", Config: "m",
		Worked:       4*time.Minute + 5*time.Second,
		Run:          view.Run{ID: "r2", Started: t0, Ended: t0.Add(time.Minute)},
		Usage:        openresponses.Usage{InputTokens: 300, OutputTokens: 50, TotalTokens: 350},
		UsageByModel: map[string]openresponses.Usage{"m": {InputTokens: 300, OutputTokens: 50, TotalTokens: 350}},
		Turn:         view.Turn{Number: 2, Attempt: 1, Cause: "max_turns"},
	}
	cost := client.Cost(func(string, openresponses.Usage) (float64, bool) { return 0.25, true })
	got := drawnLine(m, stateIdle, t0.Add(time.Hour), false, cost)
	want := "time 4m05s | tokens 300 in, 50 out, $0.2500 | model m | retry 1 | turn 2 | session 0123456789ab | unverified | last run stopped: turn limit"
	if got != want {
		t.Errorf("status\n got %q\nwant %q", got, want)
	}
}

// TestTheModelSegmentNamesTheHostsModel: the host's model is named, with
// the record's beside it while they differ.
func TestTheModelSegmentNamesTheHostsModel(t *testing.T) {
	host := ""
	c := StatusConfig{Segments: []string{"model"}, Model: func() string { return host }}
	text := func(v view.Model) string {
		ps := c.pieces(statusIn{v: v})
		if len(ps) == 0 {
			return ""
		}
		return ps[0].text
	}
	if got := text(view.Model{Config: "a"}); got != "model a" {
		t.Errorf("no host model: %q", got)
	}
	host = "b"
	if got, want := text(view.Model{Config: "a"}), "model b (record: a until the next run)"; got != want {
		t.Errorf("before a run: %q, want %q", got, want)
	}
	if got := text(view.Model{}); got != "model b" {
		t.Errorf("an empty record: %q", got)
	}
	if got := text(view.Model{Config: "b"}); got != "model b" {
		t.Errorf("after a run: %q", got)
	}
}

// TestPowerlineInkReadsOnItsBlock (#53): the powerline line draws a
// colored block's text light on a dark block and dark on a light one,
// as a terminal shows it, and the uncolored block keeps its light text.
func TestPowerlineInkReadsOnItsBlock(t *testing.T) {
	light, dark := lipgloss.Color("15"), lipgloss.Color("0")
	cases := []struct {
		text, color string
		ink         color.Color
	}{
		{"black", "0", light},
		{"blue", "4", dark},
		{"green", "2", dark},
		{"yellow", "3", dark},
		{"darkhex", "#202020", light},
		{"lighthex", "#f0f0f0", dark},
		{"plain", "", light},
	}
	var ps []piece
	for _, c := range cases {
		ps = append(ps, piece{name: c.text, text: c.text, color: c.color})
	}
	m := &Model{width: 120, status: StatusConfig{Powerline: true}}
	term := termtest.New(120, 2)
	defer term.Close()
	if _, err := term.Write([]byte(m.drawLine("", ps, ""))); err != nil {
		t.Fatal(err)
	}
	row := []rune(strings.SplitN(term.Screen(), "\n", 2)[0])
	same := func(a, b color.Color) bool {
		if a == nil || b == nil {
			return a == b
		}
		ar, ag, ab, _ := a.RGBA()
		br, bg, bb, _ := b.RGBA()
		return ar == br && ag == bg && ab == bb
	}
	for _, c := range cases {
		x := strings.Index(string(row), " "+c.text+" ")
		if x < 0 {
			t.Fatalf("%s: not on the line %q", c.text, string(row))
		}
		x = len([]rune(string(row)[:x])) + 1
		fg, _ := term.Colors(x, 0)
		if !same(fg, c.ink) {
			t.Errorf("%s on %q: ink %v, want %v", c.text, c.color, fg, c.ink)
		}
	}
}

// TestInkOnJudgesTheBlock (#53): the basic colors by the agnoster
// split (black text on blue, as agnoster draws it), 16 to 255 and #rrggbb by luminance.
func TestInkOnJudgesTheBlock(t *testing.T) {
	cases := map[string]string{
		// 12 is xterm's #5c5cff, luminance 0.17. 4 is light in many
		// terminals' palettes, and agnoster draws black on it.
		"0": "15", "1": "15", "5": "15", "8": "15", "12": "15",
		"2": "0", "3": "0", "4": "0", "6": "0", "7": "0", "9": "0", "13": "0", "15": "0",
		"16": "15", "21": "15", "231": "0", "46": "0", "232": "15", "255": "0",
		"#202020": "15", "#f0f0f0": "0", "#0000ff": "15", "#ffff00": "0",
		"bogus": "0",
	}
	for bg, want := range cases {
		if got := inkOn(bg); got != want {
			t.Errorf("inkOn(%q) = %q, want %q", bg, got, want)
		}
	}
}

// TestTheStoppedSegmentNamesTheProvidersEnds (#54): a run the content
// filter stopped, or whose answer the output limit cut, is not shown as
// a plain finish.
func TestTheStoppedSegmentNamesTheProvidersEnds(t *testing.T) {
	c := StatusConfig{Segments: []string{"stopped"}}
	for cause, want := range map[agentturn.StopCause]string{
		agentturn.StopContentFilter:   "last run stopped: the provider refused",
		agentturn.StopMaxOutputTokens: "last run stopped: answer cut at the output limit",
	} {
		ps := c.pieces(statusIn{v: view.Model{Turn: view.Turn{Cause: cause}}})
		if len(ps) != 1 || ps[0].text != want {
			t.Errorf("%s: %+v, want %q", cause, ps, want)
		}
	}
}

// TestARefusalPartFollowsTheMessagesText (#54): a message with text and
// a refusal part shows both, the refusal marked.
func TestARefusalPartFollowsTheMessagesText(t *testing.T) {
	msg := &openresponses.Message{Role: openresponses.RoleAssistant, Content: openresponses.Contents{
		&openresponses.OutputText{Text: "Sure,"}, &openresponses.Refusal{Refusal: "I can't help with that."},
	}}
	got := ansi.Strip(renderRow(view.Row{Item: msg}, opts{}, nil, "", 60, newMarkdown(), asks{}))
	if !strings.Contains(got, "Sure,") || !strings.Contains(got, "refusal: I can't help with that.") ||
		strings.Index(got, "Sure,") > strings.Index(got, "refusal:") {
		t.Errorf("row:\n%s", got)
	}
}

// TestACutResponseShowsUnderAnotherStop (#54): a stop the hook made
// after a response the output limit cut still says the answer was cut.
func TestACutResponseShowsUnderAnotherStop(t *testing.T) {
	cut := &openresponses.IncompleteDetails{Reason: openresponses.IncompleteReasonMaxOutputTokens}
	for name, m := range map[string]view.Model{
		"live":   {Run: view.Run{ID: "r"}, Turn: view.Turn{RunID: "r", Cause: agentturn.StopHook, Incomplete: cut}},
		"record": {Run: view.Run{ID: "r", Ended: time.Unix(1, 0), Cause: agentturn.StopHook, Incomplete: cut}},
	} {
		if got := ansi.Strip(runEndNote(m)); got != "✂ the answer was cut at the model's output limit" {
			t.Errorf("%s: %q", name, got)
		}
	}
}

// TestANewRunClearsTheStoppedSegment (#54): once the next run starts,
// before its start entry lands, the line no longer says how the last
// one, still the record's last, stopped.
func TestANewRunClearsTheStoppedSegment(t *testing.T) {
	c := StatusConfig{Segments: []string{"stopped"}}
	m := view.Model{Run: view.Run{ID: "r1", Ended: time.Unix(1, 0), Cause: agentturn.StopContentFilter},
		Turn: view.Turn{State: view.Running, RunID: "r2"}}
	if ps := c.pieces(statusIn{v: m}); len(ps) != 0 {
		t.Errorf("running r2: %+v, want no stopped segment", ps)
	}
	m.Turn = view.Turn{State: view.Idle, RunID: "r1", Cause: agentturn.StopContentFilter}
	if ps := c.pieces(statusIn{v: m}); len(ps) != 1 || ps[0].text != "last run stopped: the provider refused" {
		t.Errorf("idle after r1: %+v", ps)
	}
}
