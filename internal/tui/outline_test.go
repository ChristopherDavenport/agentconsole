package tui_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ChristopherDavenport/agenttool"

	"github.com/ChristopherDavenport/agentconsole/internal/tui"
	"github.com/ChristopherDavenport/agentconsole/toolview"
	"github.com/ChristopherDavenport/agentconsole/view"
)

// quietTool is a tool whose calls say nothing: its output is empty.
func quietTool(name string) agenttool.Tool {
	return agenttool.New(name, "does it", func(context.Context, textArgs) (string, error) { return "", nil })
}

// quietApp is an app whose model makes three calls in three responses,
// all quiet, then answers.
func quietApp(t *testing.T, opts ...tui.Option) *app {
	return quietAppOf(t, "touch", opts...)
}

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
	a.waitFor("the answer", all(has("all done", "idle"), lacks("(streaming)", "(not committed yet)")))
	return a
}

func TestAChainOfQuietCallsIsOneLine(t *testing.T) {
	a := quietApp(t)
	s := a.screen()
	if !strings.Contains(s, "ran 2 mkdir, 1 touch · 3 turns") {
		t.Fatalf("no summary line:\n%s", s)
	}
	if strings.Contains(s, `mkdir text=`) || strings.Contains(s, "touch text=") {
		t.Errorf("a collapsed group draws its members:\n%s", s)
	}
}

func TestCtrlOExpandsAGroupAndCollapsesIt(t *testing.T) {
	a := quietApp(t)
	a.press("ctrl+p", "ctrl+p", "ctrl+p") // the answer, its header, then the group: one stop
	if l := lineWith(a.screen(), "ran 2 mkdir"); !strings.Contains(l, "▶") {
		t.Fatalf("the cursor is not on the group:\n%s", a.screen())
	}
	a.key("ctrl+o")
	s := a.screen()
	for _, want := range []string{`mkdir text="a"`, `mkdir text="b"`, `touch text="c"`, "ran 2 mkdir, 1 touch · 3 turns", "ctrl+o to collapse"} {
		if !strings.Contains(s, want) {
			t.Errorf("expanded: missing %q:\n%s", want, s)
		}
	}
	// Expanded, the turns' headers are back between the members, and the
	// members are stops after the group's line.
	if s := a.screen(); !strings.Contains(s, "turn 1 · 1 call") || !strings.Contains(s, "turn 3 · 1 call") {
		t.Errorf("no headers between the members:\n%s", s)
	}
	a.press("ctrl+n", "ctrl+n", "ctrl+n") // turn 1's header, turn 2's, the second member
	if l := lineWith(a.screen(), `mkdir text="b"`); !strings.Contains(l, "▶") {
		t.Errorf("the cursor did not step to the second member:\n%s", a.screen())
	}
	a.press("ctrl+p", "ctrl+p", "ctrl+p")
	a.key("ctrl+o")
	if s := a.screen(); strings.Contains(s, `mkdir text="a"`) || !strings.Contains(s, "ctrl+o to expand") {
		t.Errorf("not collapsed again:\n%s", s)
	}
}

func TestClickingAGroupExpandsIt(t *testing.T) {
	a := quietApp(t)
	a.clickOn("ran 2 mkdir")
	a.clickOn("ran 2 mkdir")
	if s := a.screen(); !strings.Contains(s, `mkdir text="b"`) {
		t.Fatalf("a second click did not expand the group:\n%s", s)
	}
}

func TestOneStopForACollapsedGroup(t *testing.T) {
	a := quietApp(t)
	// answer, its header, group, the user's prompt: four stops, and no
	// member of the group between them.
	a.press("ctrl+p", "ctrl+p", "ctrl+p", "ctrl+p")
	if l := lineWith(a.screen(), "user (unclaimed)"); !strings.Contains(l, "▶") {
		t.Errorf("the third stop up is not the prompt:\n%s", a.screen())
	}
}

func TestCopyingACollapsedGroupCopiesItsLine(t *testing.T) {
	a := quietApp(t)
	a.drag("ran 2 mkdir", 0, 40)
	a.key("ctrl+c")
	got := a.copied()
	if len(got) != 1 || !strings.Contains(got[0], "ran 2 mkdir, 1 touch · 3 turns") || strings.Contains(got[0], "text=") {
		t.Errorf("copied %q", got)
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

func TestAGroupOfOneToolTakesItsSummarizer(t *testing.T) {
	a := quietAppOf(t, "mkdir", tui.WithToolSummarizers(toolview.Summarizers{"mkdir": dirs{}}))
	s := a.screen()
	if !strings.Contains(s, "made a b c") {
		t.Errorf("no summarizer line:\n%s", s)
	}
	if strings.Contains(s, "ran 3 mkdir") {
		t.Errorf("the client's line is drawn beside the summarizer's:\n%s", s)
	}
}

func TestAMixedGroupHasTheClientsLine(t *testing.T) {
	a := quietApp(t, tui.WithToolSummarizers(toolview.Summarizers{"mkdir": dirs{}}))
	if s := a.screen(); !strings.Contains(s, "ran 2 mkdir, 1 touch · 3 turns") || strings.Contains(s, "made") {
		t.Errorf("a group of two tools used one's summarizer:\n%s", s)
	}
}

func TestADecliningOrPanickingSummarizerFallsBack(t *testing.T) {
	for name, sum := range map[string]toolview.Summarizer{"declines": declines{}, "panics": panics{}} {
		a := quietAppOf(t, "mkdir", tui.WithToolSummarizers(toolview.Summarizers{"mkdir": sum}))
		if s := a.screen(); !strings.Contains(s, "ran 3 mkdir · 3 turns") || strings.Contains(s, "not drawn") {
			t.Errorf("%s: no fallback line:\n%s", name, s)
		}
	}
}

// outputApp is an app whose model calls upper twice, in two responses,
// and answers: three turns with outputs, so no group.
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
	s := a.waitFor("the answer", all(has("all done", "idle"), lacks("(streaming)", "(not committed yet)")))
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
	s := a.waitFor("the answer", all(has("sorry", "idle"), lacks("(streaming)", "(not committed yet)")))
	if !strings.Contains(s, "turn 1 · 1 call → 1 output · 1 failed") {
		t.Errorf("no failed count:\n%s", s)
	}
}

func TestFoldingATurnToItsHeader(t *testing.T) {
	a := outputApp(t, nil)
	a.waitFor("the answer", all(has("all done", "idle"), lacks("(streaming)", "(not committed yet)")))
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
		t.Errorf("a second click did not fold:\n%s", s)
	}
}

func TestACopiedFoldedTurnIsItsHeader(t *testing.T) {
	a := outputApp(t, nil)
	a.waitFor("the answer", all(has("all done", "idle"), lacks("(streaming)", "(not committed yet)")))
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
	a.waitFor("running", has("[running]", "turn 1"))
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

func TestACollapsedGroupStandsForTheHeadersItSwallows(t *testing.T) {
	a := quietApp(t)
	s := a.screen()
	for _, gone := range []string{"turn 1 ·", "turn 2 ·", "turn 3 ·"} {
		if strings.Contains(s, gone) {
			t.Errorf("a collapsed group leaves %q:\n%s", gone, s)
		}
	}
	if !strings.Contains(s, "turn 4 · answer") {
		t.Errorf("the answer has no header:\n%s", s)
	}
}
