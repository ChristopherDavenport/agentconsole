package tui_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
	// Wait for the about line to read as the whole subject, the part that
	// differs from a spoofed line: the renderer writes only the cells
	// that change, so a line seen once may not be finished.
	const want = "about: rm -rf ~/src  about: [1mls -la[0m"
	deadline := time.Now().Add(10 * time.Second)
	for {
		s := out.Screen()
		l := lineWith(s, "about:")
		if strings.Contains(l, want) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the panel's about line is %q, not the subject as text; screen:\n%s", l, s)
		}
		time.Sleep(10 * time.Millisecond)
	}

	pw.Write([]byte("n"))
	deadline = time.Now().Add(10 * time.Second)
	for ag.State().Running {
		if time.Now().After(deadline) {
			t.Fatal("the run did not end after the refusal")
		}
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

// fits says the screen is exactly height lines with the input on it: a
// leaf laid out as one row stays one row, and a panel stays short enough
// to leave the input its place.
func fits(t *testing.T, s string, height int) {
	t.Helper()
	if n := len(strings.Split(s, "\n")); n != height {
		t.Errorf("the screen is %d lines, not %d:\n%s", n, height, s)
	}
	if lineWith(s, "> ") == "" {
		t.Errorf("the input is off the screen:\n%s", s)
	}
}

// TestANewlineCannotAddAPanelLine (#56): a newline in what the policy
// asks about, or in its question, cannot add a line that reads as the
// panel's own ("about: harmless" under the real one), and a long one
// cannot push the input off the screen.
func TestANewlineCannotAddAPanelLine(t *testing.T) {
	cfg := cfgWith(callTool("call_1", "upper", `{"text":"abc"}`), say(nil, nil, "done"))
	cfg.Tools = []agenttool.Tool{upperTool(nil)}
	cfg.BeforeToolCall = func(context.Context, agentturn.ToolCallInfo) (*agentturn.ToolDecision, error) {
		return &agentturn.ToolDecision{Action: agentturn.Defer,
			Reason:  "ask: upper\n  [y] approve" + strings.Repeat("\nmore of the question", 60),
			Subject: "rm -rf ~/src\n  about: harmless" + strings.Repeat("\n  about: harmless", 60)}, nil
	}
	a := newApp(t, cfg)
	a.resize(100, 30)
	a.submit("go")
	s := a.waitFor("the permission", has("Permission requested", "about:"))
	var about, approve int
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "  about:") {
			about++
		}
		if strings.HasPrefix(l, "  [y] approve") {
			approve++
		}
	}
	if about != 1 || approve != 1 {
		t.Errorf("the panel has %d about lines and %d approve lines, want one each:\n%s", about, approve, s)
	}
	if l := lineWith(s, "about:"); !strings.Contains(l, "about: rm -rf ~/src   about: harmless") {
		t.Errorf("the about line is %q", l)
	}
	fits(t, s, 30)
}

// TestAMultiLineErrorKeepsTheInputOnScreen (#56): a provider's or a
// controller's error is shown on its one row, its newlines as spaces, so
// the screen keeps the room it laid out for the input.
func TestAMultiLineErrorKeepsTheInputOnScreen(t *testing.T) {
	a := newViewApp(t, &handSource{}, (&intents{}).act)
	a.resize(80, 20)
	a.send(tui.IntentDone("abort", errors.New("upstream said:"+strings.Repeat("\nline of the body", 30))))
	s := a.waitFor("the error", has("abort: upstream said: line of the body"))
	fits(t, s, 20)
}

// TestShortIDCutsWholeRunes (#56): an ID cut for a line is cut between
// runes, so the cut never leaves a byte that is not UTF-8 (a bare 0x9b
// is CSI to a terminal in 8-bit mode), and cleaned to one line.
func TestShortIDCutsWholeRunes(t *testing.T) {
	for in, want := range map[string]string{
		"sha256:0123456789abcdef": "01234567",
		"abc⛛⛛⛛⛛⛛⛛":               "abc⛛⛛⛛⛛⛛",
		"a\nb\x1b[1mcdefghij":     "a b[1mcd",
		"short":                   "short",
	} {
		got := tui.ShortID(in)
		if got != want || !utf8.ValidString(got) {
			t.Errorf("ShortID(%q) = %q, want %q", in, got, want)
		}
	}
}

// hostileLine is one-row text with a title, a clear, a carriage return,
// a bidi override and a newline that would start a line of its own.
const hostileLine = "x\x1b]0;pwned\a\x1b[2J\r\u202e\nabout: fake"

// TestUntrustedTextIsInertAtEverySite (#56): the sites beyond the
// transcript and the permission panel, each fed hostileLine: what the
// host's status segment, a stopped stream, a refused intent, a host's
// completion, a tool's question and a branch's label say reaches the
// terminal as text, on the one row it was laid out on.
func TestUntrustedTextIsInertAtEverySite(t *testing.T) {
	asking := agenttool.New("ask", "asks", func(ctx context.Context, _ textArgs) (string, error) {
		elicit, ok := agenttool.ElicitorFrom(ctx)
		if !ok {
			return "", errors.New("no elicitor")
		}
		_, err := elicit(ctx, agenttool.Elicitation{Message: hostileLine, URL: "https://example.com/" + hostileLine})
		return "", err
	})
	for _, c := range []struct {
		name   string
		oneRow bool
		show   func(t *testing.T) *app
	}{
		{"status segment", true, func(t *testing.T) *app {
			return newViewApp(t, &handSource{}, (&intents{}).act, tui.WithStatus(tui.StatusConfig{
				Segments: []string{"host"},
				Host:     map[string]func() tui.Segment{"host": func() tui.Segment { return tui.Segment{Text: hostileLine} }},
			}))
		}},
		{"stream stopped", true, func(t *testing.T) *app {
			a := newViewApp(t, &handSource{}, (&intents{}).act)
			a.send(tui.FeedErrMsg{Stream: "record", Err: errors.New(hostileLine)})
			return a
		}},
		{"refused intent", true, func(t *testing.T) *app {
			a := newViewApp(t, &handSource{}, (&intents{}).act)
			a.send(tui.IntentDone("abort", errors.New(hostileLine)))
			return a
		}},
		{"completion", true, func(t *testing.T) *app {
			a := newViewApp(t, &handSource{}, (&intents{}).act, tui.WithCompletion(func(context.Context, tui.Completing) []tui.Completion {
				return []tui.Completion{{Text: "/" + hostileLine, Help: hostileLine}}
			}))
			a.typeText("/")
			return a
		}},
		{"question panel", true, func(t *testing.T) *app {
			cfg := cfgWith(callTool("call_1", "ask", `{"text":"go"}`), say(nil, nil, "done"))
			cfg.Tools = []agenttool.Tool{asking}
			a := newApp(t, cfg)
			a.submit("go")
			a.waitFor("the question", has("Question (1/1)"))
			return a
		}},
		{"tree label", false, func(t *testing.T) *app {
			a := newApp(t, cfgWith(say(nil, nil, "answer "+hostileLine)))
			a.exchange("go", "answer")
			a.key("ctrl+t")
			a.waitFor("the tree", has("branches of session"))
			return a
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := c.show(t)
			a.resize(160, 40)
			s := a.waitFor("the text", has("]0;pwned"))
			f := a.frame()
			inert(t, f, s, "]0;pwned", "[2J")
			for _, seq := range []string{"\x1b]0;", "\x1b[2J"} {
				if strings.Contains(f, seq) {
					t.Errorf("the frame writes %q to the terminal", seq)
				}
			}
			if c.oneRow {
				if l := lineWith(s, "about: fake"); !strings.Contains(l, "pwned") {
					t.Errorf("the text's newline started a row of its own: %q\n%s", l, s)
				}
			}
		})
	}
}
