package tui_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ChristopherDavenport/agenttool"
)

// quietTool is a tool whose calls say nothing: its output is empty.
func quietTool(name string) agenttool.Tool {
	return agenttool.New(name, "does it", func(context.Context, textArgs) (string, error) { return "", nil })
}

// quietApp is an app whose model makes three calls in three responses,
// all quiet, then answers.
func quietApp(t *testing.T) *app {
	t.Helper()
	cfg := cfgWith(
		callTool("call_1", "mkdir", `{"text":"a"}`),
		callTool("call_2", "mkdir", `{"text":"b"}`),
		callTool("call_3", "touch", `{"text":"c"}`),
		say(nil, nil, "all done"),
	)
	cfg.Tools = []agenttool.Tool{quietTool("mkdir"), quietTool("touch")}
	a := newApp(t, cfg)
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
	a.press("ctrl+p", "ctrl+p") // the answer, then the group: one stop
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
	// Expanded, the members are stops after the group's line.
	a.key("ctrl+n")
	if l := lineWith(a.screen(), `mkdir text="b"`); !strings.Contains(l, "▶") {
		t.Errorf("the cursor did not step to the second member:\n%s", a.screen())
	}
	a.key("ctrl+p")
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
	// answer, group, the user's prompt: three stops, and no member of the
	// group between them.
	a.press("ctrl+p", "ctrl+p", "ctrl+p")
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
