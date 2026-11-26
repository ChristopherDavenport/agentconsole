package tui_test

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/internal/tui"
	"github.com/ChristopherDavenport/agentconsole/toolview"
	"github.com/ChristopherDavenport/agentconsole/view"
)

// quietTool is a tool whose calls say nothing: its output is empty.
func quietTool(name string) agenttool.Tool {
	return agenttool.New(name, "does it", func(context.Context, textArgs) (string, error) { return "", nil })
}

// gatedQuiet is a quiet tool that holds at the gate "tool" until released.
func gatedQuiet(name string, g *gates) agenttool.Tool {
	return agenttool.New(name, "does it", func(ctx context.Context, _ textArgs) (string, error) {
		return "", g.hold(ctx, "tool")
	})
}

// twice plays two steps in one response.
func twice(a, b step) step {
	return func(ctx context.Context, em *openresponses.Emitter) error {
		if err := a(ctx, em); err != nil {
			return err
		}
		return b(ctx, em)
	}
}

func settled(a *app, what string) string {
	a.t.Helper()
	return a.waitFor(what, all(has(what, "idle"), lacks("(streaming)", "(not committed yet)")))
}

// quietApp is an app whose model makes three calls in three responses,
// all quiet, then answers: turns 1 to 3 are quiet through and through.
func quietApp(t *testing.T, opts ...tui.Option) *app { return quietAppOf(t, "touch", opts...) }

// quietAppOf is quietApp with the third call to tool third.
func quietAppOf(t *testing.T, third string, opts ...tui.Option) *app {
	t.Helper()
	cfg := cfgWith(
		callTool("call_1", "mkdir", `{"text":"a"}`),
		callTool("call_2", "mkdir", `{"text":"b"}`),
		callTool("call_3", third, `{"text":"c"}`),
		say(nil, nil, "all done"),
	)
	cfg.Tools = []agenttool.Tool{quietTool("mkdir"), quietTool("touch")}
	a := newAppWith(t, cfg, nil, nil, opts...)
	a.resize(110, 50)
	a.submit("go")
	settled(a, "all done")
	return a
}

func TestFullyQuietTurnsAreOneLine(t *testing.T) {
	a := quietApp(t)
	s := a.screen()
	if !strings.Contains(s, "turns 1–3 · ran 2 mkdir, 1 touch") || !strings.Contains(s, "turn 4 · answer") {
		t.Fatalf("no run line:\n%s", s)
	}
	if strings.Contains(s, "text=") || strings.Contains(s, "turn 1 ·") || strings.Contains(s, "turn 2 ·") {
		t.Errorf("a collapsed run draws what it covers:\n%s", s)
	}
}

func TestExpandingARunShowsTurnsAsTurnsAreDrawn(t *testing.T) {
	a := quietApp(t)
	a.press("ctrl+p", "ctrl+p", "ctrl+p") // the answer, its header, the run: one stop
	if l := lineWith(a.screen(), "turns 1–3"); !strings.Contains(l, "▶") {
		t.Fatalf("the cursor is not on the run:\n%s", a.screen())
	}
	a.key("ctrl+o")
	s := a.screen()
	for _, want := range []string{"turn 1 · 1 call → 1 output", "turn 2 · 1 call → 1 output", "turn 3 · 1 call → 1 output", "ran 1 mkdir", "ran 1 touch", "▾ turns 1–3", "ctrl+o to collapse"} {
		if !strings.Contains(s, want) {
			t.Errorf("expanded: missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "text=") {
		t.Errorf("an expanded run draws calls, not summaries:\n%s", s)
	}
	// Each turn's summary expands to its call.
	a.press("ctrl+n", "ctrl+n") // turn 1's header, its summary
	a.key("ctrl+o")
	if s := a.screen(); !strings.Contains(s, `mkdir text="a"`) || strings.Contains(s, `mkdir text="b"`) {
		t.Errorf("turn 1's summary did not expand alone:\n%s", s)
	}
	a.key("ctrl+o")
	if s := a.screen(); strings.Contains(s, "text=") {
		t.Errorf("not collapsed again:\n%s", s)
	}
	// And the run collapses from its own line.
	a.press("ctrl+p", "ctrl+p")
	a.key("ctrl+o")
	if s := a.screen(); strings.Contains(s, "turn 1 ·") || !strings.Contains(s, "ctrl+o to expand") {
		t.Errorf("the run did not collapse:\n%s", s)
	}
}

func TestClickingARunExpandsIt(t *testing.T) {
	a := quietApp(t)
	a.clickOn("turns 1–3")
	a.clickOn("turns 1–3")
	if s := a.screen(); !strings.Contains(s, "turn 2 · 1 call") {
		t.Fatalf("a second click did not expand the run:\n%s", s)
	}
}

func TestOneStopForACollapsedRun(t *testing.T) {
	a := quietApp(t)
	// answer, its header, the run, the user's prompt: four stops, none
	// of the turns between.
	a.press("ctrl+p", "ctrl+p", "ctrl+p", "ctrl+p")
	if l := lineWith(a.screen(), "user (unclaimed)"); !strings.Contains(l, "▶") {
		t.Errorf("the fourth stop up is not the prompt:\n%s", a.screen())
	}
}

func TestCopyingACollapsedRunCopiesItsLine(t *testing.T) {
	a := quietApp(t)
	a.drag("turns 1–3", 0, 60)
	a.key("ctrl+c")
	got := a.copied()
	if len(got) != 1 || !strings.Contains(got[0], "turns 1–3 · ran 2 mkdir, 1 touch") || strings.Contains(got[0], "text=") {
		t.Errorf("copied %q", got)
	}
}

func TestAOneCallTurnIsAHeaderAndASummary(t *testing.T) {
	cfg := cfgWith(
		twice(say(nil, nil, "starting"), callTool("call_1", "mkdir", `{"text":"a"}`)),
		callTool("call_2", "touch", `{"text":"b"}`),
		callTool("call_3", "mkdir", `{"text":"c"}`),
		say(nil, nil, "all done"),
	)
	cfg.Tools = []agenttool.Tool{quietTool("mkdir"), quietTool("touch")}
	a := newApp(t, cfg)
	a.resize(110, 50)
	a.submit("go")
	s := settled(a, "all done")
	// The turn with a message is not in a run, even with one quiet call;
	// the two quiet turns after it are.
	for _, want := range []string{"turn 1 · 1 call → 1 output", "starting", "○ ran 1 mkdir ", "turns 2–3 · ran 1 touch, 1 mkdir"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "turn 2 ·") {
		t.Errorf("turn 2 has a header inside the run:\n%s", s)
	}
}

func TestQuietLoudQuietKeepsItsOrderOnTheScreen(t *testing.T) {
	cfg := cfgWith(
		twice(callTool("call_1", "mkdir", `{"text":"a"}`), twice(callTool("call_2", "upper", `{"text":"abc"}`), callTool("call_3", "touch", `{"text":"c"}`))),
		say(nil, nil, "all done"),
	)
	cfg.Tools = []agenttool.Tool{quietTool("mkdir"), quietTool("touch"), upperTool(nil)}
	a := newApp(t, cfg)
	a.resize(110, 50)
	a.submit("go")
	s := settled(a, "all done")
	i, j, k := strings.Index(s, "ran 1 mkdir"), strings.Index(s, `upper text="abc"`), strings.Index(s, "ran 1 touch")
	if i < 0 || j < 0 || k < 0 || !(i < j && j < k) {
		t.Errorf("quiet, loud, quiet is not summary, row, summary (%d %d %d):\n%s", i, j, k, s)
	}
}

func TestARunningQuietCallIsNeverHidden(t *testing.T) {
	g := newGates()
	cfg := cfgWith(callTool("call_1", "mkdir", `{"text":"a"}`), callTool("call_2", "mkdir", `{"text":"b"}`), say(nil, nil, "all done"))
	cfg.Tools = []agenttool.Tool{quietTool("touch"), gatedQuiet("mkdir", g)}
	a := newApp(t, cfg)
	a.resize(110, 50)
	t.Cleanup(func() { g.release("tool") })
	a.submit("go")
	g.arrive(t, "tool")
	s := a.waitFor("running", has("[running]"))
	if !strings.Contains(s, "mkdir") || strings.Contains(s, "ran 1 mkdir") {
		t.Errorf("the running call is summarized:\n%s", s)
	}
}

// outputApp is an app whose model calls upper twice, in two responses,
// and answers: three turns with outputs, so no run.
func outputApp(t *testing.T, g *gates) *app {
	t.Helper()
	cfg := cfgWith(callTool("call_1", "upper", `{"text":"abc"}`), callTool("call_2", "upper", `{"text":"def"}`), say(nil, nil, "all done"))
	cfg.Tools = []agenttool.Tool{upperTool(g)}
	a := newApp(t, cfg)
	a.resize(110, 50)
	a.submit("go")
	return a
}

func TestEveryResponseHasAHeader(t *testing.T) {
	a := outputApp(t, nil)
	s := settled(a, "all done")
	for _, want := range []string{"turn 1 · 1 call → 1 output", "turn 2 · 1 call → 1 output", "turn 3 · answer"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "turn 0") {
		t.Errorf("the prompt has a header:\n%s", s)
	}
}

func TestAFailedCallIsCountedInItsHeader(t *testing.T) {
	cfg := cfgWith(callTool("call_1", "boom", `{"text":"a"}`), say(nil, nil, "sorry"))
	cfg.Tools = []agenttool.Tool{agenttool.New("boom", "fails", func(context.Context, textArgs) (string, error) {
		return "", errors.New("it broke")
	})}
	a := newApp(t, cfg)
	a.resize(110, 50)
	a.submit("go")
	s := settled(a, "sorry")
	if !strings.Contains(s, "turn 1 · 1 call → 1 output · 1 failed") {
		t.Errorf("no failed count:\n%s", s)
	}
}

func TestFoldingATurnToItsHeader(t *testing.T) {
	a := outputApp(t, nil)
	settled(a, "all done")
	// Stops from the end: answer, its header, call 2, its header.
	a.press("ctrl+p", "ctrl+p", "ctrl+p", "ctrl+p")
	if l := lineWith(a.screen(), "turn 2 ·"); !strings.Contains(l, "▶") {
		t.Fatalf("the cursor is not on turn 2's header:\n%s", a.screen())
	}
	a.key("ctrl+o")
	s := a.screen()
	if !strings.Contains(s, "▸ turn 2 · 1 call → 1 output (ctrl+o to expand)") || strings.Contains(s, `upper text="def"`) {
		t.Fatalf("turn 2 is not folded to its header:\n%s", s)
	}
	if !strings.Contains(s, `upper text="abc"`) || !strings.Contains(s, "all done") {
		t.Errorf("the other turns are folded too:\n%s", s)
	}
	// Folded, the turn is one stop: up goes past it to the call before.
	a.key("ctrl+p")
	if l := lineWith(a.screen(), `upper text="abc"`); !strings.Contains(l, "▶") {
		t.Errorf("the cursor did not step over the folded turn:\n%s", a.screen())
	}
	a.key("ctrl+n")
	a.key("ctrl+o")
	if s := a.screen(); !strings.Contains(s, `upper text="def"`) || strings.Contains(s, "ctrl+o to expand") {
		t.Errorf("not unfolded:\n%s", s)
	}
	// The header is selected, so a click on it folds it too.
	a.clickOn("turn 2 ·")
	if s := a.screen(); !strings.Contains(s, "▸ turn 2") {
		t.Errorf("a click did not fold:\n%s", s)
	}
}

func TestACopiedFoldedTurnIsItsHeader(t *testing.T) {
	a := outputApp(t, nil)
	settled(a, "all done")
	a.press("ctrl+p", "ctrl+p", "ctrl+p", "ctrl+p", "ctrl+o")
	a.drag("▸ turn 2", 0, 60)
	a.key("ctrl+c")
	if got := a.copied(); len(got) != 1 || !strings.Contains(got[0], "turn 2 · 1 call → 1 output") || strings.Contains(got[0], "def") {
		t.Errorf("copied %q", got)
	}
}

func TestATurnWithARunningCallIsNeverFolded(t *testing.T) {
	g := newGates()
	a := outputApp(t, g)
	t.Cleanup(func() { g.release("tool") })
	g.arrive(t, "tool")
	// The header itself, not "turn 1": the status line says that before the
	// live row has its response, and the header is a stop the presses count.
	a.waitFor("running", has("[running]", "turn 1 · 1 call"))
	a.press("ctrl+p", "ctrl+p") // the call, then the live turn's header
	a.key("ctrl+o")
	s := a.screen()
	if !strings.Contains(s, "[running]") || strings.Contains(s, "▸ turn 1") {
		t.Errorf("a turn with a running call was folded:\n%s", s)
	}
	if !strings.Contains(s, "still going") {
		t.Errorf("no note:\n%s", s)
	}
}

// covered is the turn numbers a screen accounts for: a header names one,
// a run's line names a range.
func covered(screen string) map[int]bool {
	got := map[int]bool{}
	for _, m := range regexp.MustCompile(`turn (\d+) ·`).FindAllStringSubmatch(screen, -1) {
		n, _ := strconv.Atoi(m[1])
		got[n] = true
	}
	for _, m := range regexp.MustCompile(`turns (\d+)–(\d+)`).FindAllStringSubmatch(screen, -1) {
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		for n := a; n <= b; n++ {
			got[n] = true
		}
	}
	return got
}

// A quiet chain across turns, a turn of two quiet calls, a call with
// output, a failed call and an answer: no turn number goes unexplained
// however the lines are folded and expanded.
func TestNoTurnNumberIsMissingFromTheScreen(t *testing.T) {
	cfg := cfgWith(
		callTool("c1", "mkdir", `{"text":"a"}`),
		callTool("c2", "mkdir", `{"text":"b"}`),
		callTool("c3", "touch", `{"text":"c"}`),
		callTool("c4", "upper", `{"text":"abc"}`),
		twice(callTool("c5", "mkdir", `{"text":"d"}`), callTool("c6", "touch", `{"text":"e"}`)),
		callTool("c7", "boom", `{"text":"f"}`),
		say(nil, nil, "all done"),
	)
	cfg.Tools = []agenttool.Tool{quietTool("mkdir"), quietTool("touch"), upperTool(nil),
		agenttool.New("boom", "fails", func(context.Context, textArgs) (string, error) { return "", errors.New("it broke") })}
	a := newApp(t, cfg)
	a.resize(110, 70)
	a.submit("go")
	settled(a, "all done")
	check := func(when string) {
		t.Helper()
		s := a.screen()
		got := covered(s)
		for n := 1; n <= 7; n++ {
			if !got[n] {
				t.Errorf("%s: turn %d is not on the screen:\n%s", when, n, s)
			}
		}
	}
	check("collapsed")
	if s := a.screen(); !strings.Contains(s, "turns 1–3 · ran 2 mkdir, 1 touch") || !strings.Contains(s, "turn 5 · 2 calls → 2 outputs") || !strings.Contains(s, "ran 1 mkdir, 1 touch") {
		t.Errorf("the lines are not as expected:\n%s", s)
	}
	// Fold turn 4, then expand the run, then the summaries.
	a.clickOn("turn 4 ·")
	a.clickOn("turn 4 ·")
	check("turn 4 folded")
	a.clickOn("turns 1–3")
	a.clickOn("turns 1–3")
	check("run expanded")
	for _, g := range []string{"ran 1 mkdir, 1 touch", "ran 1 touch"} {
		a.clickOn(g)
		a.clickOn(g)
	}
	check("summaries expanded")
	if s := a.screen(); !strings.Contains(s, `touch text="c"`) || !strings.Contains(s, `touch text="e"`) {
		t.Errorf("the summaries did not expand:\n%s", s)
	}
}

// dirs sums up its calls by the text argument of each.
type dirs struct{}

func (dirs) Summarize(calls []view.Call) (toolview.Line, bool) {
	var names []string
	for _, c := range calls {
		var a textArgs
		if json.Unmarshal([]byte(c.Args), &a) != nil {
			return nil, false
		}
		names = append(names, a.Text)
	}
	return toolview.Line{toolview.S(toolview.Plain, "made "), toolview.S(toolview.Emphasis, strings.Join(names, " "))}, true
}

type declines struct{}

func (declines) Summarize([]view.Call) (toolview.Line, bool) {
	return toolview.Line{toolview.S(toolview.Plain, "not drawn")}, false
}

type panics struct{}

func (panics) Summarize([]view.Call) (toolview.Line, bool) { panic("summarize") }

func TestARunOfOneToolTakesItsSummarizer(t *testing.T) {
	a := quietAppOf(t, "mkdir", tui.WithToolSummarizers(toolview.Summarizers{"mkdir": dirs{}}))
	s := a.screen()
	if !strings.Contains(s, "turns 1–3 · made a b c") || strings.Contains(s, "ran 3 mkdir") {
		t.Errorf("no summarizer line:\n%s", s)
	}
}

func TestAMixedRunHasTheClientsLine(t *testing.T) {
	a := quietApp(t, tui.WithToolSummarizers(toolview.Summarizers{"mkdir": dirs{}}))
	if s := a.screen(); !strings.Contains(s, "ran 2 mkdir, 1 touch") || strings.Contains(s, "made") {
		t.Errorf("a run of two tools used one's summarizer:\n%s", s)
	}
}

func TestADecliningOrPanickingSummarizerFallsBack(t *testing.T) {
	for name, sum := range map[string]toolview.Summarizer{"declines": declines{}, "panics": panics{}} {
		a := quietAppOf(t, "mkdir", tui.WithToolSummarizers(toolview.Summarizers{"mkdir": sum}))
		if s := a.screen(); !strings.Contains(s, "turns 1–3 · ran 3 mkdir") || strings.Contains(s, "not drawn") {
			t.Errorf("%s: no fallback line:\n%s", name, s)
		}
	}
}

// shortOut is a tool whose calls print a few lines: quiet by size, so the
// summary says how many lines it folded.
func shortOut(name, out string) agenttool.Tool {
	return agenttool.New(name, "prints", func(context.Context, textArgs) (string, error) { return out, nil })
}

func TestASummaryCountsTheLinesItFolded(t *testing.T) {
	cfg := cfgWith(
		callTool("call_1", "ls", `{"text":"a"}`),
		callTool("call_2", "status", `{"text":"b"}`),
		say(nil, nil, "all done"),
	)
	cfg.Tools = []agenttool.Tool{shortOut("ls", "a.txt\nb.txt"), shortOut("status", "M x.go")}
	a := newApp(t, cfg)
	a.resize(110, 50)
	a.submit("go")
	s := settled(a, "all done")
	if !strings.Contains(s, "turns 1–2 · ran 1 ls, 1 status · 3 lines out") || strings.Contains(s, "a.txt") {
		t.Fatalf("no run line with the folded lines:\n%s", s)
	}
	// Expanded, a turn's summary says its own, and expanding that shows
	// the call as a row, its output behind the row's own hint.
	a.press("ctrl+p", "ctrl+p", "ctrl+p") // the answer, its header, the run
	a.key("ctrl+o")
	a.press("ctrl+n", "ctrl+n") // turn 1's header, its summary
	if s := a.screen(); !strings.Contains(s, "ran 1 ls · 2 lines out") {
		t.Fatalf("no summary under turn 1:\n%s", s)
	}
	a.key("ctrl+o")
	s = a.screen()
	if !strings.Contains(s, `ls text="a"`) || !strings.Contains(s, "2 lines (ctrl+o)") {
		t.Errorf("the call is not a row with its output hint:\n%s", s)
	}
	a.key("ctrl+n") // the call's row; its output is behind ctrl+o as ever
	a.key("ctrl+o")
	if s := a.screen(); !strings.Contains(s, "a.txt") || !strings.Contains(s, "b.txt") {
		t.Errorf("the output is not shown expanded:\n%s", s)
	}
}

func TestALongOutputIsALoudRowAmongQuietOnes(t *testing.T) {
	cfg := cfgWith(
		twice(callTool("call_1", "ls", `{"text":"a"}`), twice(callTool("call_2", "cat", `{"text":"b"}`), callTool("call_3", "ls", `{"text":"c"}`))),
		say(nil, nil, "all done"),
	)
	cfg.Tools = []agenttool.Tool{shortOut("ls", "a.txt"), shortOut("cat", "1\n2\n3\n4\n5\n6")}
	a := newApp(t, cfg)
	a.resize(110, 50)
	a.submit("go")
	s := settled(a, "all done")
	i, j, k := strings.Index(s, "ran 1 ls · 1 line out"), strings.Index(s, `cat text="b"`), strings.LastIndex(s, "ran 1 ls · 1 line out")
	if i < 0 || j < 0 || !(i < j && j < k) || !strings.Contains(s, "6 lines (ctrl+o)") {
		t.Errorf("summary, loud cat, summary expected (%d %d %d):\n%s", i, j, k, s)
	}
}

// Reasoning between quiet calls folds with them, and expanding the run
// shows it where it was, collapsed by default and in full with
// WithReasoningShown; Ctrl-R on the row flips it either way.
func TestAQuietRunsReasoningFollowsTheSetting(t *testing.T) {
	for _, tc := range []struct {
		name  string
		opts  []tui.Option
		shown bool
	}{
		{"collapsed by default", nil, false},
		{"shown with the option", []tui.Option{tui.WithReasoningShown()}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := cfgWith(
				think("checking the dir", callTool("call_1", "mkdir", `{"text":"a"}`)),
				say(nil, nil, "all done"),
			)
			cfg.Tools = []agenttool.Tool{quietTool("mkdir")}
			a := newAppWith(t, cfg, nil, nil, tc.opts...)
			a.resize(110, 50)
			a.submit("go")
			s := settled(a, "all done")
			if !strings.Contains(s, "ran 1 mkdir") || strings.Contains(s, "reasoning") || strings.Contains(s, "checking the dir") {
				t.Fatalf("the reasoning did not fold with its call:\n%s", s)
			}
			a.press("ctrl+p", "ctrl+p", "ctrl+p") // the answer, its header, the quiet run
			if l := lineWith(a.screen(), "ran 1 mkdir"); !strings.Contains(l, "▶") {
				t.Fatalf("the cursor is not on the quiet run:\n%s", a.screen())
			}
			a.key("ctrl+o")
			collapsed := func(s string) bool {
				return strings.Contains(s, "reasoning (16 chars") && !strings.Contains(s, "checking the dir")
			}
			expanded := func(s string) bool {
				return strings.Contains(s, "▾ reasoning") && strings.Contains(s, "checking the dir")
			}
			want, other := collapsed, expanded
			if tc.shown {
				want, other = expanded, collapsed
			}
			if s := a.screen(); !want(s) || !strings.Contains(s, `mkdir text="a"`) {
				t.Errorf("expanded run:\n%s", s)
			}
			a.key("ctrl+n") // the reasoning row
			a.key("ctrl+r")
			if s := a.screen(); !other(s) {
				t.Errorf("ctrl+r did not flip the row:\n%s", s)
			}
		})
	}
}
