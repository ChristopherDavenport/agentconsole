package tui_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ChristopherDavenport/agenttool"

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
	s = a.waitFor("the text grown in place", has("hello "+cursor))
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

func TestReasoningIsCollapsedUntilToggled(t *testing.T) {
	a := newApp(t, cfgWith(think("secret musings", say(nil, nil, "answer"))))
	a.submit("q")
	s := a.waitFor("the answer", all(has("answer", "idle")))
	if strings.Contains(s, "secret musings") || !strings.Contains(s, "reasoning (14 chars") {
		t.Errorf("reasoning is not collapsed:\n%s", s)
	}
	a.key(tea.KeyCtrlR)
	a.waitFor("the reasoning expanded", has("secret musings"))
	a.key(tea.KeyCtrlR)
	a.waitFor("the reasoning collapsed again", lacks("secret musings"))
}

func TestToolCallShowsItsStates(t *testing.T) {
	g := newGates()
	cfg := cfgWith(callTool("call_1", "upper", `{"text":"abc"}`), say(nil, nil, "done"))
	cfg.Tools = []agenttool.Tool{upperTool(g)}
	a := newApp(t, cfg)
	t.Cleanup(func() { g.release("tool") })

	a.submit("go")
	g.arrive(t, "tool")
	s := a.waitFor("the call running", has("upper [running]", `{"text":"abc"}`, "... working"))
	if strings.Contains(s, "ABC") {
		t.Errorf("output shown before the tool returned:\n%s", s)
	}

	g.release("tool")
	s = a.waitFor("the call ended", all(has("upper [ended]", "ABC", "done", "idle"), lacks("(live)")))
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
	s := a.waitFor("the call ended", has("lines [ended]", "idle", "ok"))
	if strings.Contains(s, "l4") || !strings.Contains(s, "2 more lines") {
		t.Errorf("output not collapsed:\n%s", s)
	}
	a.key(tea.KeyCtrlO)
	a.waitFor("the output expanded", has("l4", "l5"))
}

func TestApprovingAPermissionResumesTheRun(t *testing.T) {
	cfg := cfgWith(callTool("call_1", "upper", `{"text":"abc"}`), say(nil, nil, "done"))
	cfg.Tools = []agenttool.Tool{upperTool(nil)}
	cfg.BeforeToolCall = deferAll("may I run upper?")
	a := newApp(t, cfg)

	a.submit("go")
	s := a.waitFor("the permission", has("Permission requested", "upper", "may I run upper?", "[y] approve", "[n] refuse", "requires action", "upper [deferred]"))
	if !strings.Contains(s, "(1/1)") {
		t.Errorf("no count in the panel:\n%s", s)
	}

	a.typeText("y")
	s = a.waitFor("the run finished", all(has("upper [ended]", "ABC", "done", "idle"), lacks("Permission requested")))
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
	a.key(tea.KeyEnter)
	s := a.waitFor("the refusal recorded", all(has("upper [ended]", "The user refused this call. Reason: too risky", "idle"), lacks("Permission requested")))
	if strings.Contains(s, "ABC") {
		t.Errorf("the refused call ran:\n%s", s)
	}
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
	a.key(tea.KeyEsc)
	a.waitFor("back at the question", has("[y] approve"))
	a.typeText("n")
	a.key(tea.KeyEnter)
	a.waitFor("the plain refusal", has("The user refused this call.", "idle"))
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
	a.key(tea.KeyCtrlC)
	a.waitFor("the run ended", has("idle"))
	if a.quitted() {
		t.Fatal("ctrl+c quit a running program instead of aborting")
	}
	if s := a.screen(); strings.Contains(s, "never") {
		t.Errorf("the aborted stream went on:\n%s", s)
	}
	a.key(tea.KeyCtrlC)
	a.waitQuit()
}

func TestResize(t *testing.T) {
	long := strings.Repeat("word ", 40)
	a := newApp(t, cfgWith(say(nil, nil, long)))
	a.submit("hi")
	a.waitFor("the answer", has("word", "idle"))

	for _, size := range []tea.WindowSizeMsg{{Width: 40, Height: 12}, {Width: 120, Height: 40}, {Width: 20, Height: 6}} {
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
	a.key(tea.KeyHome)
	a.waitFor("the top", has("you"))
	a.key(tea.KeyEnd)
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
	a.waitFor("the run finished", all(has("ABC", "done", "idle"), lacks("Permission requested")))
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
