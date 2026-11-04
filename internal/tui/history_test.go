package tui_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/ChristopherDavenport/agentconsole/internal/tui"
)

// historyApp is an app given a history to start from, recording each
// line sent.
func historyApp(t *testing.T, past []string) (*app, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var sent []string
	record := func(line string) {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, line)
	}
	a := newAppWith(t, cfgWith(say(nil, nil, "r1"), say(nil, nil, "r2")), nil, nil, tui.WithHistory(past, record))
	return a, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), sent...)
	}
}

// inputLine is the input's line on the screen.
func inputLine(s string) string { return lineWith(s, "> ") }

// Up on the input's first line walks back through what was sent, newest
// first, the host's history after this session's lines; Down walks
// forward and brings the draft back. A line is never sent without Enter,
// and every line sent is handed to the host to keep (#44).
func TestUpAndDownWalkTheHistory(t *testing.T) {
	a, sent := historyApp(t, []string{"from before"})
	a.submit("first prompt")
	a.waitFor("r1", has("r1", "idle"))
	a.submit("second prompt")
	a.waitFor("r2", has("r2", "idle"))
	if got := sent(); len(got) != 2 || got[0] != "first prompt" || got[1] != "second prompt" {
		t.Fatalf("recorded %q", got)
	}

	a.typeText("a draft")
	a.key("up")
	a.waitFor("the newest", func(s string) bool { return strings.Contains(inputLine(s), "second prompt") })
	a.key("up")
	a.waitFor("the older", func(s string) bool { return strings.Contains(inputLine(s), "first prompt") })
	a.key("up")
	a.waitFor("the host's", func(s string) bool { return strings.Contains(inputLine(s), "from before") })
	a.key("up") // nothing older: stays
	a.waitFor("still the host's", func(s string) bool { return strings.Contains(inputLine(s), "from before") })
	a.key("down")
	a.key("down")
	a.waitFor("the newest again", func(s string) bool { return strings.Contains(inputLine(s), "second prompt") })
	a.key("down")
	s := a.waitFor("the draft back", func(s string) bool { return strings.Contains(inputLine(s), "a draft") })
	if strings.Count(s, "r2") != 1 || len(sent()) != 2 {
		t.Errorf("walking the history sent something:\n%s", s)
	}
}

// In a multi-line input Up moves the cursor up a line until it is on
// the first, and only then walks the history.
func TestUpMovesThroughAMultiLineInputFirst(t *testing.T) {
	a, _ := historyApp(t, []string{"from before"})
	a.typeText("top")
	a.key("ctrl+j")
	a.typeText("bottom")
	a.key("up")
	s := a.screen()
	if strings.Contains(s, "from before") {
		t.Fatalf("Up on the second line walked the history:\n%s", s)
	}
	a.key("up")
	a.waitFor("the history", func(s string) bool { return strings.Contains(inputLine(s), "from before") })
	a.key("down")
	a.waitFor("the draft back", has("top", "bottom"))
}
