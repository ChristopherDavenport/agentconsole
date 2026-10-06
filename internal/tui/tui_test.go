package tui_test

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/internal/tui"
)

const cursor = "▍"

func TestStreamingTextUpdatesInPlaceThenCommitsOnce(t *testing.T) {
	g := newGates()
	a := newApp(t, cfgWith(say(g, map[int]string{0: "one", 1: "two"}, "hel", "lo ", "world")))
	t.Cleanup(func() { g.release("one"); g.release("two") })

	a.submit("hi")
	g.arrive(t, "one")
	s := a.waitFor("the first chunk streaming", has("hel"+cursor, "(streaming)", "running", "turn 1", "model scripted"))
	if !strings.Contains(s, "you") || !strings.Contains(s, "hi") {
		t.Errorf("the prompt is not shown:\n%s", s)
	}

	g.release("one")
	g.arrive(t, "two")
	// The reply is markdown, and a paragraph's trailing space is not
	// drawn.
	s = a.waitFor("the text grown in place", has("hello"+cursor))
	if n := strings.Count(s, "assistant"); n != 1 {
		t.Errorf("assistant label shown %d times while streaming, want 1:\n%s", n, s)
	}

	g.release("two")
	s = a.waitFor("the message committed", all(has("hello world", "idle"), lacks("(streaming)", cursor, "(not committed yet)")))
	if n := strings.Count(s, "hello world"); n != 1 {
		t.Errorf("hello world shown %d times, want 1:\n%s", n, s)
	}
	if n := strings.Count(s, "assistant"); n != 1 {
		t.Errorf("assistant label shown %d times, want 1:\n%s", n, s)
	}
}

func TestTheReplyIsRenderedMarkdown(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "# Plan\n\n- **one**\n- `two`\n\n```\nthree\n```")))
	a.submit("go")
	a.waitFor("the reply rendered", all(has("Plan", "• one", "two", "  three", "idle"), lacks("# Plan", "**", "`")))
}

func TestRunningTokenTotalShowsOnTheStatusLine(t *testing.T) {
	a := newApp(t, cfgWith(usageSay(100, 20, "hi")))
	a.submit("go")
	a.waitFor("the running token total", all(has("tokens 100 in, 20 out", "hi", "idle")))
}

func TestReasoningIsCollapsedUntilToggled(t *testing.T) {
	a := newApp(t, cfgWith(think("secret musings", say(nil, nil, "answer"))))
	a.submit("q")
	s := a.waitFor("the answer", all(has("answer", "idle")))
	if strings.Contains(s, "secret musings") || !strings.Contains(s, "reasoning (14 chars") {
		t.Errorf("reasoning is not collapsed:\n%s", s)
	}
	a.key("ctrl+r")
	a.waitFor("the reasoning expanded", has("secret musings"))
	a.key("ctrl+r")
	a.waitFor("the reasoning collapsed again", lacks("secret musings"))
}

// spinFrames are the spinner's frames, one rune each.
const spinFrames = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

func TestTheRunLineAndARunningCallTurnOneSpinner(t *testing.T) {
	g := newGates()
	cfg := cfgWith(callTool("call_1", "upper", `{"text":"abc"}`), say(nil, nil, "done"))
	cfg.Tools = []agenttool.Tool{upperTool(g)}
	a := newApp(t, cfg)
	t.Cleanup(func() { g.release("tool") })

	a.submit("go")
	g.arrive(t, "tool")
	s := a.waitFor("the call running", has(`upper text="abc" [running]`))
	// The run line is above the blank line, the input and its two bars.
	frame := func(s string) (run string, spin rune) {
		lines := strings.Split(s, "\n")
		run = lines[len(lines)-5]
		r := []rune(run)
		return run, r[len(r)-1]
	}
	run, spin := frame(s)
	if !strings.HasPrefix(run, "● running") || !strings.ContainsRune(spinFrames, spin) {
		t.Fatalf("the run line is not running with the spinner at its end: %q", run)
	}
	if lipgloss.Width(run) != 100 {
		t.Errorf("the spinner is not at the right edge: %q", run)
	}
	// The call's row reads as the run line does: a dot, the text in the
	// column "running" starts in, and the same spinner at the same edge.
	row := strings.Split(s, "\n")[lineOf(s, `upper text="abc"`)]
	if !strings.HasPrefix(row, `● upper text="abc" [running]`) {
		t.Errorf("the running call is not dotted as the run line is: %q", row)
	}
	if r := []rune(row); r[len(r)-1] != spin || lipgloss.Width(row) != lipgloss.Width(run) {
		t.Errorf("the running call's spinner is not under the run line's %q:\n%s\n%s", spin, row, run)
	}
	a.waitFor("the spinner turning", func(s string) bool { _, r := frame(s); return r != spin })

	g.release("tool")
	a.waitFor("the call ended", all(has(`○ upper text="abc"`, "done"), lacks("[running]")))
	s = a.waitFor("the run line idle", func(s string) bool { run, _ := frame(s); return run == "○ idle" })
	if strings.ContainsAny(s, spinFrames) {
		t.Errorf("a spinner is left after the run:\n%s", s)
	}
}

func TestTheRunLineShowsTheTurnsTimeAndTokens(t *testing.T) {
	g := newGates()
	call := callTool("call_1", "upper", `{"text":"abc"}`)
	cfg := cfgWith(func(ctx context.Context, em *openresponses.Emitter) error {
		em.Response().Usage = &openresponses.Usage{InputTokens: 1500, OutputTokens: 40, TotalTokens: 1540}
		return call(ctx, em)
	}, usageSay(2000, 60, "done"))
	cfg.Tools = []agenttool.Tool{upperTool(g)}
	a := newApp(t, cfg)
	t.Cleanup(func() { g.release("tool") })

	a.submit("go")
	g.arrive(t, "tool")
	// The first model call is on the record by the time its call runs.
	figures := regexp.MustCompile(`^● running \((\d+)s · 1\.5k↑ / 40↓\) +[` + spinFrames + `]$`)
	s := a.waitFor("the turn's figures", func(s string) bool {
		ls := strings.Split(s, "\n")
		return figures.MatchString(ls[len(ls)-5])
	})
	session := regexp.MustCompile(`^time \d+s \| tokens 1\.5k in, 40 out`)
	if top := strings.Split(s, "\n")[0]; !session.MatchString(top) {
		t.Errorf("the status line does not lead with the session's time: %q", top)
	}
	g.release("tool")
	s = a.waitFor("the run over", has("○ idle", "done"))
	if strings.Contains(s, "↑") {
		t.Errorf("idle shows a turn's figures:\n%s", s)
	}
	// The session's figures stay on the status line, the two calls summed.
	if !regexp.MustCompile(`^time \d+s \| tokens 3\.5k in, 100 out`).MatchString(strings.Split(s, "\n")[0]) {
		t.Errorf("the status line lost the session's tokens:\n%s", s)
	}
}

func TestToolCallShowsItsStates(t *testing.T) {
	g := newGates()
	cfg := cfgWith(callTool("call_1", "upper", `{"text":"abc"}`), say(nil, nil, "done"))
	cfg.Tools = []agenttool.Tool{upperTool(g)}
	a := newApp(t, cfg)
	t.Cleanup(func() { g.release("tool") })

	a.submit("go")
	g.arrive(t, "tool")
	s := a.waitFor("the call running", has(`upper text="abc" [running]`, "...", "working"))
	if strings.Contains(s, "ABC") {
		t.Errorf("output shown before the tool returned:\n%s", s)
	}

	g.release("tool")
	a.waitFor("the call ended", all(has(`upper text="abc"`, `1 line (ctrl+o)`, "done", "idle"), lacks("(live)", "ABC", "[running]")))
	a.key("ctrl+o")
	s = a.waitFor("the output expanded", has("ABC"))
	if n := strings.Count(s, "ABC"); n != 1 {
		t.Errorf("output shown %d times, want 1 (the call row carries it):\n%s", n, s)
	}
}

func TestLongToolOutputCollapsesUntilToggled(t *testing.T) {
	long := agenttool.New("lines", "many lines", func(_ context.Context, _ textArgs) (string, error) {
		return "l1\nl2\nl3\nl4\nl5", nil
	})
	cfg := cfgWith(callTool("c", "lines", `{"text":"x"}`), say(nil, nil, "ok"))
	cfg.Tools = []agenttool.Tool{long}
	a := newApp(t, cfg)
	a.submit("go")
	s := a.waitFor("the call ended", all(has(`lines text="x"`, `5 lines (ctrl+o)`, "idle", "ok")))
	if strings.Contains(s, "l1") {
		t.Errorf("output not collapsed:\n%s", s)
	}
	a.key("ctrl+o")
	a.waitFor("the output expanded", has("l4", "l5"))
}

// A long argument value is clipped on the row's line, so the content of
// a write never pushes the conversation aside.
func TestALongArgumentIsClippedOnTheRowLine(t *testing.T) {
	long := strings.Repeat("x", 300)
	cfg := cfgWith(callTool("c", "write", `{"path":"a.txt","content":"`+long+`"}`), say(nil, nil, "ok"))
	cfg.Tools = []agenttool.Tool{agenttool.New("write", "writes", func(_ context.Context, _ textArgs) (string, error) {
		return "", nil
	})}
	a := newApp(t, cfg)
	a.submit("go")
	s := a.waitFor("the call ended", has(`write content="`+strings.Repeat("x", 60), "idle"))
	if strings.Contains(s, strings.Repeat("x", 61)) {
		t.Errorf("the argument value was not clipped:\n%s", s)
	}
}

// twoCallsApp is an app whose model calls lines twice, with "p" and then
// "q", each answered with five lines named after its text.
func twoCallsApp(t *testing.T) *app {
	t.Helper()
	long := agenttool.New("lines", "many lines", func(_ context.Context, a textArgs) (string, error) {
		return a.Text + "1\n" + a.Text + "2\n" + a.Text + "3\n" + a.Text + "4\n" + a.Text + "5", nil
	})
	cfg := cfgWith(callTool("c1", "lines", `{"text":"p"}`), callTool("c2", "lines", `{"text":"q"}`), say(nil, nil, "ok"))
	cfg.Tools = []agenttool.Tool{long}
	a := newApp(t, cfg)
	a.resize(110, 50)
	a.submit("go")
	a.waitFor("both calls ended", all(has(`text="p"`, `text="q"`, "ok", "idle"), lacks("p3", "q3", "(not committed yet)")))
	return a
}

func TestCtrlOOnASelectedRowExpandsThatRowAlone(t *testing.T) {
	a := twoCallsApp(t)
	a.press("ctrl+p", "ctrl+p", "ctrl+p") // the answer, the second call, the first
	a.key("ctrl+o")
	a.waitFor("the first call expanded", all(has("p4", "p5"), lacks("q4")))
	a.key("ctrl+o")
	a.waitFor("the first call collapsed", lacks("p4", "q4"))
	// With no row selected, ctrl+o is for every row again.
	a.key("ctrl+o")
	a.key("esc")
	a.key("ctrl+o")
	a.waitFor("every call expanded", has("p4", "q4"))
	// A row's own flip goes when every row is toggled.
	a.key("ctrl+o")
	a.waitFor("every call collapsed", lacks("p4", "q4"))
}

// click left-clicks the screen line holding sub: a press and a release
// with no motion between, which is what selects a row.
func (a *app) click(sub string) {
	a.t.Helper()
	for y, l := range strings.Split(a.screen(), "\n") {
		if strings.Contains(l, sub) {
			a.send(tea.MouseClickMsg{X: 4, Y: y, Button: tea.MouseLeft})
			a.send(tea.MouseReleaseMsg{X: 4, Y: y, Button: tea.MouseLeft})
			return
		}
	}
	a.t.Fatalf("no line holds %q:\n%s", sub, a.screen())
}

// drag presses at x0 on the line holding sub, moves to x1 on it, and
// releases: a selection of the columns x0..x1 of that line.
func (a *app) drag(sub string, x0, x1 int) string {
	a.t.Helper()
	for y, l := range strings.Split(a.screen(), "\n") {
		if strings.Contains(l, sub) {
			return a.dragCells(x0, y, x1, y)
		}
	}
	a.t.Fatalf("no line holds %q:\n%s", sub, a.screen())
	return ""
}

// dragCells presses at (x0, y0), moves to (x1, y1), and releases.
func (a *app) dragCells(x0, y0, x1, y1 int) string {
	a.send(tea.MouseClickMsg{X: x0, Y: y0, Button: tea.MouseLeft})
	a.send(tea.MouseMotionMsg{X: x1, Y: y1, Button: tea.MouseLeft})
	a.send(tea.MouseReleaseMsg{X: x1, Y: y1, Button: tea.MouseLeft})
	return a.screen()
}

// cursorLine is the line the row cursor rests on, marked with ▶.
func cursorLine(screen string) string {
	for _, l := range strings.Split(screen, "\n") {
		if strings.Contains(l, "▶") {
			return l
		}
	}
	return ""
}

func TestClickingARowSelectsItAndClickingAgainExpandsIt(t *testing.T) {
	a := twoCallsApp(t)
	a.click(`text="p"`)
	if s := a.screen(); !strings.Contains(cursorLine(s), `text="p"`) || strings.Contains(s, "p4") {
		t.Fatalf("the click did not just select the first call:\n%s", s)
	}
	a.click(`text="p"`)
	a.waitFor("the first call expanded", all(has("p4", "p5"), lacks("q4")))
	// Clicking another row moves the cursor and expands nothing.
	a.click(`text="q"`)
	if s := a.screen(); !strings.Contains(cursorLine(s), `text="q"`) || strings.Contains(s, "q4") {
		t.Fatalf("the click on the second call did not just select it:\n%s", s)
	}
	a.key("tab")
	a.waitFor("the second call's detail", has("record detail", "call c2 lines"))
}

func TestClickingTheStatusLineSelectsNothing(t *testing.T) {
	a := twoCallsApp(t)
	a.send(tea.MouseClickMsg{X: 4, Y: 0, Button: tea.MouseLeft})
	a.send(tea.MouseReleaseMsg{X: 4, Y: 0, Button: tea.MouseLeft})
	if s := a.screen(); strings.Contains(s, "▶") {
		t.Fatalf("a row was selected:\n%s", s)
	}
}

func TestApprovingAPermissionResumesTheRun(t *testing.T) {
	cfg := cfgWith(callTool("call_1", "upper", `{"text":"abc"}`), say(nil, nil, "done"))
	cfg.Tools = []agenttool.Tool{upperTool(nil)}
	cfg.BeforeToolCall = deferAll("may I run upper?")
	a := newApp(t, cfg)

	a.submit("go")
	s := a.waitFor("the permission", has("Permission requested", "upper", "may I run upper?", "[y] approve", "[n] refuse", "requires action", `text="abc" [deferred]`))
	if !strings.Contains(s, "(1/1)") {
		t.Errorf("no count in the panel:\n%s", s)
	}

	a.typeText("y")
	s = a.waitFor("the run finished", all(has(`upper text="abc"`, `1 line (ctrl+o)`, "done", "idle"), lacks("Permission requested")))
	_ = s
}

func TestRefusingAPermissionRecordsTheReason(t *testing.T) {
	cfg := cfgWith(callTool("call_1", "upper", `{"text":"abc"}`))
	cfg.Tools = []agenttool.Tool{upperTool(nil)}
	cfg.BeforeToolCall = deferAll("")
	a := newApp(t, cfg)

	a.submit("go")
	a.waitFor("the permission", has("Permission requested"))
	a.typeText("n")
	a.waitFor("the reason prompt", has("Reason for refusing"))
	a.typeText("too risky")
	a.key("enter")
	s := a.waitFor("the refusal recorded", all(has(`upper text="abc"`, `1 line (ctrl+o)`, "idle"), lacks("Permission requested")))
	if strings.Contains(s, "ABC") {
		t.Errorf("the refused call ran:\n%s", s)
	}
	// The refusal's text is the call's output: hidden collapsed, and
	// expanded by ctrl+o.
	a.key("ctrl+o")
	a.waitFor("the refusal's text", has("The user refused this call. Reason: too risky"))
}

func TestRefusingWithNoReasonAndGoingBack(t *testing.T) {
	cfg := cfgWith(callTool("call_1", "upper", `{"text":"abc"}`))
	cfg.Tools = []agenttool.Tool{upperTool(nil)}
	cfg.BeforeToolCall = deferAll("")
	a := newApp(t, cfg)
	a.submit("go")
	a.waitFor("the permission", has("Permission requested"))
	a.typeText("n")
	a.waitFor("the reason prompt", has("Reason for refusing"))
	a.key("esc")
	a.waitFor("back at the question", has("[y] approve"))
	a.typeText("n")
	a.key("enter")
	a.waitFor("the plain refusal", all(has(`upper text="abc"`, `1 line (ctrl+o)`, "idle"), lacks("Permission requested")))
}

// A paste is text, never a key: it does not answer a permission, and it
// reaches the input only once a refusal's reason is being typed.
func TestAPasteDoesNotAnswerAPermission(t *testing.T) {
	cfg := cfgWith(callTool("call_1", "upper", `{"text":"abc"}`))
	cfg.Tools = []agenttool.Tool{upperTool(nil)}
	cfg.BeforeToolCall = deferAll("")
	a := newApp(t, cfg)
	a.submit("go")
	a.waitFor("the permission", has("Permission requested"))
	a.paste("y")
	a.paste("n")
	s := a.screen()
	if !strings.Contains(s, "[y] approve") || strings.Contains(s, "Reason for refusing") {
		t.Fatalf("a paste answered the permission:\n%s", s)
	}
	if !strings.Contains(lineWith(s, "> "), "say something") {
		t.Fatalf("a paste reached the input while the permission waits:\n%s", s)
	}
	a.typeText("n")
	a.waitFor("the reason prompt", has("Reason for refusing"))
	a.paste("too risky")
	a.key("enter")
	a.waitFor("the refusal recorded", all(has(`upper text="abc"`, "idle"), lacks("Permission requested")))
	a.key("ctrl+o")
	a.waitFor("the pasted reason", has("The user refused this call. Reason: too risky"))
}

func TestEnterWhileRunningSteers(t *testing.T) {
	g := newGates()
	a := newApp(t, cfgWith(say(g, map[int]string{0: "mid"}, "first"), say(nil, nil, "second")))
	t.Cleanup(func() { g.release("mid") })

	a.submit("start")
	g.arrive(t, "mid")
	a.waitFor("running", has("running"))
	a.submit("also this")
	g.release("mid")
	s := a.waitFor("the steer on the record and answered", all(has("also this", "second", "idle")))
	if n := strings.Count(s, "also this"); n != 1 {
		t.Errorf("steer shown %d times, want 1:\n%s", n, s)
	}
}

func TestCtrlCAbortsARunAndQuitsWhenIdle(t *testing.T) {
	g := newGates()
	a := newApp(t, cfgWith(say(g, map[int]string{0: "mid"}, "partial", "never")))
	t.Cleanup(func() { g.release("mid") })

	a.submit("start")
	g.arrive(t, "mid")
	a.waitFor("running", has("partial"+cursor, "running"))
	a.key("ctrl+c")
	a.waitFor("the run ended", has("idle"))
	if a.quitted() {
		t.Fatal("ctrl+c quit a running program instead of aborting")
	}
	if s := a.screen(); strings.Contains(s, "never") {
		t.Errorf("the aborted stream went on:\n%s", s)
	}
	a.key("ctrl+c")
	a.waitQuit()
}

// A program whose output is not a terminal, given no size, reports 0x0
// at its start: the client waits for a size rather than laying out in
// none.
func TestAZeroSizeIsNotASize(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hi")))
	a.mu.Lock()
	m := tui.New(a.ctx, a.be)
	a.mu.Unlock()
	m.Update(tea.WindowSizeMsg{Width: 0, Height: 0})
	if s := m.View().Content; s != "starting..." {
		t.Fatalf("a 0x0 size was laid out:\n%s", s)
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	if s := m.View().Content; s == "starting..." {
		t.Fatal("a size did not lay the screen out")
	}
}

func TestResize(t *testing.T) {
	long := strings.Repeat("word ", 40)
	a := newApp(t, cfgWith(say(nil, nil, long)))
	a.submit("hi")
	a.waitFor("the answer", has("word", "idle"))

	for _, size := range []tea.WindowSizeMsg{{Width: 40, Height: 12}, {Width: 120, Height: 40}, {Width: 20, Height: 8}} {
		a.send(size)
		s := a.screen()
		lines := strings.Split(s, "\n")
		if len(lines) != size.Height {
			t.Errorf("%dx%d: the screen is %d lines:\n%s", size.Width, size.Height, len(lines), s)
		}
		for _, l := range lines {
			if w := lipgloss.Width(l); w > size.Width {
				t.Errorf("%dx%d: a line is %d wide: %q", size.Width, size.Height, w, l)
			}
		}
	}
}

func TestUnverifiedAndScroll(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, strings.Repeat("line of text\n\n", 60))))
	a.send(tea.WindowSizeMsg{Width: 60, Height: 10})
	a.submit("hi")
	a.waitFor("the end of the answer", has("idle"))
	if s := a.screen(); strings.Contains(s, "you") {
		t.Errorf("a long answer should have scrolled the prompt off:\n%s", s)
	}
	a.key("ctrl+home")
	a.waitFor("the top", has("you"))
	a.key("ctrl+end")
	a.waitFor("the bottom", lacks("you"))
}

func TestFeedFailureStaysOnTheStatusLine(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "ok")))
	a.send(tui.FeedErrMsg{Stream: "record", Err: errors.New("boom")})
	a.waitFor("the feed banner", has("FEED STOPPED", "record stream: boom", "resume the session"))
	// Neither a run's end nor sending nor a fresh model clears it.
	a.submit("hi")
	a.waitFor("the run done", has("ok"))
	a.send(tui.ModelMsg{})
	s := a.screen()
	if !strings.Contains(s, "FEED STOPPED") {
		t.Errorf("the feed failure was cleared:\n%s", s)
	}
}

// TestAnsweredPermissionIsNotOfferedAgainWhileTheViewLags: the run ends
// (runDoneMsg) before the slow feed has told the view, which still lists
// the permission just answered.
func TestAnsweredPermissionIsNotOfferedAgainWhileTheViewLags(t *testing.T) {
	cfg := cfgWith(callTool("call_1", "upper", `{"text":"abc"}`), say(nil, nil, "done"))
	cfg.Tools = []agenttool.Tool{upperTool(nil)}
	cfg.BeforeToolCall = deferAll("may I run upper?")
	a := newApp(t, cfg)
	a.submit("go")
	a.waitFor("the permission", has("Permission requested"))

	a.feedDelay.Store(int64(200 * time.Millisecond))
	a.typeText("y")
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if s := a.screen(); strings.Contains(s, "Permission requested") {
			t.Fatalf("the answered permission was offered again:\n%s", s)
		}
		time.Sleep(2 * time.Millisecond)
	}
	a.feedDelay.Store(0)
	a.waitFor("the run finished", all(has(`1 line (ctrl+o)`, "done", "idle"), lacks("Permission requested")))
}

// TestDrainWaitsForTheRunsControlCall: after the program ends, a host
// must be able to wait for the Prompt it started to return.
func TestDrainWaitsForTheRunsControlCall(t *testing.T) {
	g := newGates()
	a := newApp(t, cfgWith(say(g, map[int]string{0: "mid"}, "partial", "more")))
	t.Cleanup(func() { g.release("mid") })
	a.submit("start")
	g.arrive(t, "mid")

	if a.m.Drain(50 * time.Millisecond) {
		t.Fatal("Drain returned while the run was in flight")
	}
	a.cancel() // what the host does last; the held script ends with the context
	if !a.m.Drain(wait) {
		t.Fatal("Drain did not see the run end")
	}
}

func TestInterruptFromOutsideActsLikeCtrlC(t *testing.T) {
	g := newGates()
	a := newApp(t, cfgWith(say(g, map[int]string{0: "mid"}, "partial", "never")))
	t.Cleanup(func() { g.release("mid") })
	a.submit("start")
	g.arrive(t, "mid")
	a.waitFor("running", has("partial"+cursor, "running"))
	a.send(tui.InterruptMsg{})
	a.waitFor("the run aborted", has("idle"))
	if a.quitted() {
		t.Fatal("an interrupt quit a running program instead of aborting")
	}
	a.send(tui.InterruptMsg{})
	a.waitQuit()
}

func TestHomeEndAndArrowsMoveTheInputCursor(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "ok")))
	a.typeText("abc")
	a.key("home")
	a.typeText("X")
	a.waitFor("X at the start", has("> Xabc"))
	a.key("end")
	a.typeText("Y")
	a.waitFor("Y at the end", has("> XabcY"))
	a.key("left")
	a.key("left")
	a.typeText("Z")
	a.waitFor("Z two from the end", has("> XabZcY"))
}

func TestLongPromptWrapsInsteadOfScrollingSideways(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hi")))
	a.exchange("hello", "hi")
	a.typeText(strings.Repeat("wordy ", 200))
	s := a.screen()
	lines := strings.Split(s, "\n")
	if len(lines) != 30 {
		t.Fatalf("the screen is %d lines on a 30-line terminal:\n%s", len(lines), s)
	}
	wrapped := 0
	for _, l := range lines {
		if w := lipgloss.Width(l); w > 100 {
			t.Errorf("a line is %d wide:\n%s", w, s)
		}
		if strings.Contains(l, "> ") && strings.Contains(l, "wordy") {
			wrapped++
		}
	}
	if wrapped < 2 {
		t.Fatalf("the long prompt did not wrap to more than one line:\n%s", s)
	}
}

func TestPastedNewlinesCountAsRows(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hi")))
	a.exchange("hello", "hi")
	a.paste("first line\nsecond line\nthird line\nfourth line")
	s := a.screen()
	lines := strings.Split(s, "\n")
	if len(lines) != 30 {
		t.Fatalf("the screen is %d lines on a 30-line terminal:\n%s", len(lines), s)
	}
	// The paste keeps its newlines: each pasted line is its own screen
	// row, one after the other, not one long row.
	row := -1
	for i, l := range lines {
		if strings.Contains(l, "first line") {
			row = i
			break
		}
	}
	if row == -1 {
		t.Fatalf("the paste is not visible:\n%s", s)
	}
	if row+3 >= len(lines) {
		t.Fatalf("the paste did not expand to four rows:\n%s", s)
	}
	for i, want := range []string{"second line", "third line", "fourth line"} {
		if !strings.Contains(lines[row+1+i], want) {
			t.Fatalf("the pasted lines share one row; %q is not the row after the last:\n%s", want, s)
		}
	}
}

func TestTheInputSitsBetweenTwoBarsUnderTheRunLineAndTheScreenStillFits(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hi")))
	a.exchange("hello", "hi")
	bar := strings.Repeat("─", 100)
	s := a.screen()
	lines := strings.Split(s, "\n")
	if len(lines) != 30 {
		t.Fatalf("the screen is %d lines on a 30-line terminal:\n%s", len(lines), s)
	}
	n := len(lines)
	if !strings.Contains(lines[n-2], "> ") || !strings.Contains(lines[n-3], bar) || !strings.Contains(lines[n-1], bar) {
		t.Fatalf("the input is not between two bars:\n%s", s)
	}
	if strings.TrimSpace(lines[n-4]) != "" {
		t.Fatalf("no blank line above the input:\n%s", s)
	}
	if lines[n-5] != "○ idle" {
		t.Fatalf("the run line is not above the blank line:\n%s", s)
	}
	if strings.TrimSpace(lines[n-6]) != "" {
		t.Fatalf("no blank line above the run line:\n%s", s)
	}
	// The tree has no input, so no bars.
	a.key("ctrl+t")
	if s := a.screen(); strings.Contains(s, bar) {
		t.Fatalf("the tree shows the input's bars:\n%s", s)
	}
}

func TestCtrlSlashOrF1ShowsTheKeysAndTakesNoInput(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hi")))
	a.exchange("hello", "hi")
	if s := a.screen(); !strings.Contains(s, "ctrl+/ for keys") {
		t.Fatalf("the input does not say how to get the keys:\n%s", s)
	}
	for _, open := range []string{"ctrl+/", "ctrl+_", "f1"} {
		a.key(open)
		s := a.screen()
		for _, want := range []string{"Ctrl-P Ctrl-N", "Permissions", "Tree (Ctrl-T)", "esc, q or ctrl+/ back"} {
			if !strings.Contains(s, want) {
				t.Fatalf("%v: the keys screen lacks %q:\n%s", open, want, s)
			}
		}
		if strings.Contains(s, "> ") || strings.Contains(s, "hello") {
			t.Fatalf("%v: the keys screen shows the input or the conversation:\n%s", open, s)
		}
		a.typeText("x") // typed on the keys screen, it goes nowhere
		a.paste("pasted")
		a.key(open)
		s = a.screen()
		if !strings.Contains(s, "hello") || strings.Contains(s, "Permissions") {
			t.Fatalf("%v: the keys screen did not close:\n%s", open, s)
		}
		if l := lineWith(s, "> "); strings.Contains(l, "x") || strings.Contains(l, "pasted") {
			t.Fatalf("%v: a key typed or pasted on the keys screen reached the input:\n%s", open, s)
		}
	}
	a.key("f1")
	a.rune('q')
	a.waitFor("q closes the keys", all(has("hello"), lacks("Permissions")))
}

// TestTheInputHasTheTerminalsBackground: the textarea's default styles
// give the line the cursor is on a background of their own, which shows
// as a band of another color across the input on most terminals. The
// input's rows are drawn with no background, before the terminal says
// what its own is and after, dark or light.
func TestTheInputHasTheTerminalsBackground(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hi")))
	check := func(when string) {
		t.Helper()
		a.mu.Lock()
		raw := a.m.View().Content
		a.mu.Unlock()
		for _, l := range strings.Split(raw, "\n") {
			if strings.Contains(l, "> ") && hasBackground(l) {
				t.Errorf("%s: the input is drawn with a background: %q", when, l)
			}
		}
	}
	check("at the start")
	a.typeText("typed")
	check("with text typed")
	for _, bg := range []color.Color{color.Black, color.White} {
		a.send(tea.BackgroundColorMsg{Color: bg})
		check(fmt.Sprintf("on a %v terminal", bg))
	}
}

var sgr = regexp.MustCompile("\x1b\\[([0-9;:]*)m")

// hasBackground is whether s sets a background color: an SGR 40-47,
// 100-107 or 48, the extended foreground and underline colors' own
// numbers skipped.
func hasBackground(s string) bool {
	for _, m := range sgr.FindAllStringSubmatch(s, -1) {
		ps := strings.FieldsFunc(m[1], func(r rune) bool { return r == ';' || r == ':' })
		for i := 0; i < len(ps); i++ {
			n, _ := strconv.Atoi(ps[i])
			switch {
			case n == 48, n >= 40 && n <= 47, n >= 100 && n <= 107:
				return true
			case n == 38 || n == 58:
				if i+1 < len(ps) && ps[i+1] == "5" {
					i += 2
				} else if i+1 < len(ps) && ps[i+1] == "2" {
					i += 4
				}
			}
		}
	}
	return false
}
