package tui_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/ChristopherDavenport/agentconsole/client"
	"github.com/ChristopherDavenport/agentconsole/internal/tui"
)

// hostCompletion completes a slash command at the input's start from the
// host's list, and an @path from the host's paths, and keeps what it was
// asked.
type hostCompletion struct {
	mu    sync.Mutex
	asked []tui.Completing
}

func (h *hostCompletion) complete(_ context.Context, c tui.Completing) []tui.Completion {
	h.mu.Lock()
	h.asked = append(h.asked, c)
	h.mu.Unlock()
	var out []tui.Completion
	switch {
	case strings.HasPrefix(c.Word, "/") && c.Before == c.Word:
		for _, cmd := range []tui.Completion{{Text: "/model", Help: "<name>  switch the model"}, {Text: "/mode", Help: "[NAME]  switch the mode"}, {Text: "/quit"}} {
			if strings.HasPrefix(cmd.Text, c.Word) {
				out = append(out, cmd)
			}
		}
	case strings.HasPrefix(c.Word, "@"):
		for _, p := range []string{"internal/box/box.go", "internal/box/box_test.go", "cmd/main.go"} {
			if segmentsStart(p, c.Word[1:]) {
				out = append(out, tui.Completion{Text: "@" + p})
			}
		}
	}
	return out
}

func itemJSON(t *testing.T, it any) string {
	t.Helper()
	b, err := json.Marshal(it)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// segmentsStart reports whether each slash-separated part of prefix
// starts the same part of path: int/bo starts internal/box/box.go.
func segmentsStart(path, prefix string) bool {
	ps, ws := strings.Split(path, "/"), strings.Split(prefix, "/")
	if len(ws) > len(ps) {
		return false
	}
	for i, w := range ws {
		if !strings.HasPrefix(ps[i], w) {
			return false
		}
	}
	return true
}

// TestTheInputCompletesFromTheHost: typing /mo lists the host's /model
// and /mode with their help, and Tab puts the chosen one in; @int/bo
// lists the host's paths, and the line is sent as typed; with no list
// open, Tab cycles the pane (#45).
func TestTheInputCompletesFromTheHost(t *testing.T) {
	src := &handSource{}
	ctl := &intents{}
	host := &hostCompletion{}
	a := newViewApp(t, src, ctl.act, tui.WithCompletion(host.complete))

	a.typeText("/mo")
	s := a.waitFor("the commands", has("/model", "<name>  switch the model", "/mode", "[NAME]  switch the mode"))
	if strings.Contains(s, "/quit") {
		t.Errorf("a command the word does not start was offered:\n%s", s)
	}
	a.key("down")
	a.key("tab")
	s = a.waitFor("the second completed", lacks("switch the model"))
	if in := lineWith(s, "> "); !strings.Contains(in, "/mode") || strings.Contains(in, "/model") {
		t.Fatalf("the input is %q, want /mode", in)
	}

	// Clear the input; an @path from the host's paths.
	for range len("/mode") {
		a.key("backspace")
	}
	a.typeText("look at @int/bo")
	a.waitFor("the paths", has("@internal/box/box.go", "@internal/box/box_test.go"))
	a.key("tab")
	s = a.waitFor("the path completed", lacks("box_test.go"))
	if in := lineWith(s, "> "); !strings.Contains(in, "look at @internal/box/box.go") {
		t.Fatalf("the input is %q, want the path in place of the word", in)
	}
	a.key("enter")
	in, ok := ctl.next(t, 1).(client.Prompt)
	if !ok || len(in.Items) != 1 || !strings.Contains(itemJSON(t, in.Items[0]), "look at @internal/box/box.go") {
		t.Fatalf("intent = %#v, want the line as typed", in)
	}

	// No list: Tab cycles the pane.
	a.typeText("hello")
	a.key("tab")
	a.waitFor("the pane", has("record detail"))

	// Esc closes a list, and it stays closed until the word changes.
	a.key("esc")
	for range len("hello") {
		a.key("backspace")
	}
	a.typeText("/q")
	a.waitFor("the list", has("/quit"))
	a.key("esc")
	a.waitFor("the list closed", lacks("/quit"))

	host.mu.Lock()
	defer host.mu.Unlock()
	for _, c := range host.asked {
		if c.Word == "" && !strings.HasSuffix(c.Before, " ") {
			t.Errorf("the host was asked about no word, not after a space: %+v", c)
		}
	}
}

// TestASpaceAsksAboutWhatFollows: the word after a space is asked about
// while it is still empty, so the host lists a command's subcommands
// before any is typed: after a completion that ends in a space, and
// after a space typed. The empty input is not asked about.
func TestASpaceAsksAboutWhatFollows(t *testing.T) {
	src := &handSource{}
	ctl := &intents{}
	var mu sync.Mutex
	var asked []tui.Completing
	host := func(_ context.Context, c tui.Completing) []tui.Completion {
		mu.Lock()
		asked = append(asked, c)
		mu.Unlock()
		var out []tui.Completion
		switch {
		case c.Before == c.Word && strings.HasPrefix("/mcp", c.Word) && c.Word != "":
			out = append(out, tui.Completion{Text: "/mcp ", Help: "the servers"})
		case strings.TrimSuffix(c.Before, c.Word) == "/mcp ":
			for _, sub := range []tui.Completion{{Text: "add ", Help: "start a server"}, {Text: "remove ", Help: "stop a server"}} {
				if strings.HasPrefix(sub.Text, c.Word) {
					out = append(out, sub)
				}
			}
		}
		return out
	}
	a := newViewApp(t, src, ctl.act, tui.WithCompletion(host))

	a.typeText("/m")
	a.waitFor("the command", has("/mcp", "the servers"))
	a.key("tab")
	// The completion ended in a space: what follows is listed at once.
	a.waitFor("the subcommands", both(has("start a server", "stop a server"), lacks("the servers")))
	a.key("down")
	a.key("tab")
	s := a.waitFor("the subcommand completed", lacks("stop a server"))
	if in := lineWith(s, "> "); !strings.Contains(in, "/mcp remove") {
		t.Fatalf("the input is %q, want /mcp remove", in)
	}

	// A space typed asks too, and a letter narrows the list.
	for range len("/mcp remove ") {
		a.key("backspace")
	}
	a.typeText("/mcp ")
	a.waitFor("the subcommands again", has("start a server", "stop a server"))
	a.typeText("a")
	a.waitFor("the list narrowed", both(has("start a server"), lacks("stop a server")))
	// Esc closes the list for an empty word as for any other.
	a.key("backspace")
	a.waitFor("the subcommands once more", has("stop a server"))
	a.key("esc")
	a.waitFor("the list closed", lacks("start a server"))

	mu.Lock()
	defer mu.Unlock()
	for _, c := range asked {
		if c.Before == "" {
			t.Errorf("the host was asked about an empty input: %+v", c)
		}
	}
}

// TestCompletionHelpLinesUpByWidth: the help column starts at the same
// column for every completion, measured in terminal cells, so one with
// wide characters does not push its help out of line.
func TestCompletionHelpLinesUpByWidth(t *testing.T) {
	src := &handSource{}
	ctl := &intents{}
	wide := func(_ context.Context, c tui.Completing) []tui.Completion {
		if c.Word != "/" {
			return nil
		}
		return []tui.Completion{{Text: "/日本", Help: "wide help"}, {Text: "/abc", Help: "narrow help"}}
	}
	a := newViewApp(t, src, ctl.act, tui.WithCompletion(wide))
	a.typeText("/")
	s := a.waitFor("the list", has("wide help", "narrow help"))
	col := func(sub string) int {
		l := lineWith(s, sub)
		return lipgloss.Width(l[:strings.Index(l, sub)])
	}
	if w, n := col("wide help"), col("narrow help"); w != n {
		t.Errorf("the help starts at column %d beside /日本 and %d beside /abc:\n%s", w, n, s)
	}
}

// both is a and b of the screen.
func both(a, b func(string) bool) func(string) bool {
	return func(s string) bool { return a(s) && b(s) }
}
