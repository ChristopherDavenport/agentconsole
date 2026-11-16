package tui_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentsession/jsonl"
	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"

	"github.com/ChristopherDavenport/agentconsole/client"
	"github.com/ChristopherDavenport/agentconsole/internal/termtest"
	"github.com/ChristopherDavenport/agentconsole/internal/tui"
	"github.com/ChristopherDavenport/agentconsole/toolview"
)

// frame is the screen as the model draws it, its styles kept: what the
// program hands the terminal's renderer.
func (a *app) frame() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.m.View().Content
}

// inert says the frame holds none of the controls Clean removes (a
// carriage return, a bidi control), and that each sequence's body shows
// as text on the screen: ansi.Strip takes out a whole sequence whose ESC
// is still in front of it, so the body shows only once the ESC is gone.
func inert(t *testing.T, frame, screen string, bodies ...string) {
	t.Helper()
	for _, c := range []string{"\r", "\u202e", "\u2066", "\u200f", "\a", "\u009b"} {
		if strings.Contains(frame, c) {
			t.Errorf("the frame writes %q to the terminal", c)
		}
	}
	for _, b := range bodies {
		if !strings.Contains(screen, b) {
			t.Errorf("the screen does not show %q as text:\n%s", b, screen)
		}
	}
}

// TestAPermissionSubjectCannotRewriteThePanel (#56): a carriage return
// and an SGR in the part of a call the policy asks about are shown as
// text in the permission panel, on a real terminal, rather than taking
// the cursor back over "about:" and writing a harmless command there.
func TestAPermissionSubjectCannotRewriteThePanel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := jsonl.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rec, _, err := session.Start(ctx, store, agentsession.Header{Records: agentsession.AllRecords})
	if err != nil {
		t.Fatal(err)
	}
	cfg := cfgWith(callTool("call_1", "upper", `{"text":"abc"}`), say(nil, nil, "done"))
	cfg.Tools = []agenttool.Tool{upperTool(nil)}
	cfg.BeforeToolCall = func(context.Context, agentturn.ToolCallInfo) (*agentturn.ToolDecision, error) {
		return &agentturn.ToolDecision{Action: agentturn.Defer, Reason: "ask: upper",
			Subject: "rm -rf ~/src\r  about: \x1b[1mls -la\x1b[0m            "}, nil
	}
	ag := agentturn.New(cfg)
	defer rec.Attach(ag)()
	record, err := client.RecordOf(rec)
	if err != nil {
		t.Fatal(err)
	}

	pr, pw := io.Pipe()
	defer pw.Close()
	out := termtest.New(100, 30)
	defer out.Close()
	p := tea.NewProgram(tui.New(ctx, record, ag, client.Direct(ag)), tea.WithInput(pr), tea.WithOutput(out), tea.WithContext(ctx), tea.WithWindowSize(100, 30), tea.WithColorProfile(colorprofile.ANSI))
	wait := tui.Attach(ctx, record, ag, p.Send)
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()

	pw.Write([]byte("go\r"))
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out.Screen(), "Permission requested") || !strings.Contains(out.Screen(), "about:") {
		if time.Now().After(deadline) {
			t.Fatalf("no permission panel; screen:\n%s", out.Screen())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Give the renderer a frame or two past the panel's first.
	time.Sleep(100 * time.Millisecond)
	s := out.Screen()
	if l := lineWith(s, "about:"); !strings.Contains(l, "about: rm -rf ~/src  about: [1mls -la[0m") {
		t.Errorf("the panel's about line is %q, not the subject as text; screen:\n%s", l, s)
	}

	pw.Write([]byte("n"))
	for ag.State().Running {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	pw.Write([]byte{0x03})
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("program: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ctrl+c did not end the program")
	}
	cancel()
	wait()
}

// hostile is text a model or a tool might carry: a hyperlink to
// somewhere it does not say, a screen clear and a cursor move, a window
// title, a clipboard write and a C1 CSI.
const hostile = "see \x1b]8;;x-evil:payload\x1b\\here\x1b]8;;\x1b\\ \x1b[2J\x1b[Hcleared \x1b]0;pwned\a \x1b]52;c;ZXZpbA==\a \u009b31mred"

// TestModelAndToolTextCannotWriteControlSequences (#56): an OSC 8 link,
// a clear, a cursor move, a title and a clipboard write in what the model
// says, what it thinks and what a tool returns reach the terminal as
// text, never as sequences; the client's own styles still do.
func TestModelAndToolTextCannotWriteControlSequences(t *testing.T) {
	cfg := cfgWith(think("thought "+hostile, callTool("call_1", "leak", `{"text":"x"}`)), say(nil, nil, "said "+hostile))
	cfg.Tools = []agenttool.Tool{agenttool.New("leak", "returns hostile text", func(context.Context, textArgs) (string, error) {
		return "output " + hostile, nil
	})}
	a := newApp(t, cfg)
	a.resize(200, 60)
	a.submit("go")
	a.waitFor("the answer", all(has("said", "idle"), lacks("(streaming)", "(not committed yet)")))
	a.press("ctrl+r", "ctrl+o")
	s := a.waitFor("everything expanded", has("thought", "output", "said"))
	f := a.frame()
	inert(t, f, s, "]8;;x-evil:payload", "[2J", "]0;pwned", "]52;c;ZXZpbA==")
	for _, who := range []string{"thought", "output", "said"} {
		if l := lineWith(s, who); !strings.Contains(l, "]0;pwned") {
			t.Errorf("the %s line keeps a sequence: %q", who, l)
		}
	}
	for _, seq := range []string{"\x1b]8;;x-evil", "\x1b[2J", "\x1b[H", "\x1b]0;", "\x1b]52;"} {
		if strings.Contains(f, seq) {
			t.Errorf("the frame writes %q to the terminal", seq)
		}
	}
	if !strings.Contains(f, "\x1b[") {
		t.Error("the frame lost the client's own styles")
	}
}

// TestABidiOverrideInToolArgumentsIsRemoved (#56): a right-to-left
// override in a call's arguments, which would make the permission panel
// read a different command than the one asked about, is not written:
// not in the panel, not in a tool renderer's head, not in the expanded
// arguments.
func TestABidiOverrideInToolArgumentsIsRemoved(t *testing.T) {
	cfg := cfgWith(callTool("call_1", "upper", "{\"text\":\"abc\u202efed\u202c \u2066g\u2069\"}"), say(nil, nil, "done"))
	cfg.Tools = []agenttool.Tool{upperTool(nil)}
	cfg.BeforeToolCall = deferAll("may I run upper?")
	a := newAppWith(t, cfg, nil, nil, tui.WithToolRenderers(toolview.Renderers{"upper": upperRenderer{}}))
	a.resize(150, 50)
	a.submit("go")
	s := a.waitFor("the permission", has("Permission requested", "«abcfed g»"))
	inert(t, a.frame(), s, `{"text":"abcfed g"}`)
	a.typeText("y")
	a.waitFor("the call done", all(has("done", "idle"), lacks("(streaming)", "(not committed yet)")))
	a.key("ctrl+o")
	s = a.waitFor("the arguments", has(`args: {"text":"abcfed g"}`, "ABCFED G"))
	inert(t, a.frame(), s)
}

// TestNewlinesTabsAndUnicodeAreKept (#56): what Clean leaves is the
// text: lines stay lines, and CJK, an emoji with its skin tone and a
// combining accent are drawn as they came.
func TestNewlinesTabsAndUnicodeAreKept(t *testing.T) {
	text := "漢字 👍🏽 cafe\u0301"
	cfg := cfgWith(callTool("call_1", "leak", `{"text":"x"}`), say(nil, nil, "first "+text+"\n\nsecond line"))
	cfg.Tools = []agenttool.Tool{agenttool.New("leak", "returns lines", func(context.Context, textArgs) (string, error) {
		return "one\ttab " + text + "\ntwo", nil
	})}
	a := newApp(t, cfg)
	a.resize(150, 50)
	a.submit("go")
	a.waitFor("the answer", all(has("second line", "idle"), lacks("(streaming)", "(not committed yet)")))
	a.key("ctrl+o")
	s := a.waitFor("the output", has("two"))
	if !strings.Contains(lineWith(s, "first"), text) {
		t.Errorf("the message line lost its text: %q", lineWith(s, "first"))
	}
	if l := lineWith(s, "tab"); !strings.Contains(l, text) || strings.Contains(l, "two") {
		t.Errorf("the output's first line is %q: its text or its newline is lost", l)
	}
	if lineWith(s, "second line") == lineWith(s, "first") {
		t.Errorf("the message's paragraphs are on one line:\n%s", s)
	}
}
