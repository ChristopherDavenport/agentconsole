package tui_test

import (
	"strings"
	"testing"

	"github.com/ChristopherDavenport/agentconsole/client"
	"github.com/ChristopherDavenport/agentconsole/internal/tui"
)

// The status line at the bottom: the conversation starts on the first
// row, and the mouse, the selection and the notices follow it there.

// atBottom puts the status line on the last row.
var atBottom = tui.WithStatus(tui.StatusConfig{Bottom: true})

// rows are the screen's rows.
func rows(screen string) []string { return strings.Split(screen, "\n") }

// With the line at the bottom the conversation starts on the first row,
// and a click lands on the row under it.
func TestAtTheBottomAClickSelectsTheRowUnderIt(t *testing.T) {
	a := twoCallsApp(t, atBottom)
	for _, call := range []string{`text="q"`, `text="p"`} {
		a.click(call)
		if s := a.screen(); !strings.Contains(cursorLine(s), call) {
			t.Fatalf("the click on %s selected another row:\n%s", call, s)
		}
	}
}

// With the line at the bottom a drag copies the text it was over.
func TestAtTheBottomADragCopiesTheTextUnderIt(t *testing.T) {
	a := newAppWith(t, cfgWith(say(nil, nil, "hello world")), nil, nil, atBottom)
	a.submit("hi")
	a.waitFor("the answer", all(has("hello world", "idle"), lacks("(not committed yet)")))

	ya, yw := lineOf(a.screen(), "agent"), lineOf(a.screen(), "hello world")
	a.dragCells(0, ya, 11, yw)
	a.key("ctrl+c")
	if got, want := a.copied(), "agent\nhello world"; len(got) != 1 || got[0] != want {
		t.Errorf("copied %q, want [%q]", got, want)
	}
}

// With the line at the bottom a notice sits on its own row just over
// the line, so the conversation's first row never moves as notices come
// and go.
func TestAtTheBottomANoticeSitsOverTheLine(t *testing.T) {
	a := newAppWith(t, cfgWith(), nil, nil, atBottom)
	before := a.waitFor("the line at the bottom", func(s string) bool {
		r := rows(s)
		return strings.Contains(r[len(r)-1], "session ")
	})
	a.key("shift+tab")
	notice := "mode: " + client.ErrNoModes.Error()
	s := a.waitFor("the refusal", has(notice))
	r := rows(s)
	if len(r) != 30 {
		t.Errorf("the screen is %d rows, want 30:\n%s", len(r), s)
	}
	if !strings.Contains(r[len(r)-2], notice) || !strings.Contains(r[len(r)-1], "session ") {
		t.Errorf("the notice is not on the row over the line:\n%s", s)
	}
	if r[0] != rows(before)[0] {
		t.Errorf("the first row moved for the notice: %q, was %q", r[0], rows(before)[0])
	}
}

// With the line at the bottom a copy's note lays the screen out again:
// the frame stays the terminal's height and the line on its last row.
func TestAtTheBottomACopyKeepsTheLineOnScreen(t *testing.T) {
	a := newAppWith(t, cfgWith(say(nil, nil, "hello world")), nil, nil, atBottom)
	a.submit("hi")
	a.waitFor("the answer", all(has("hello world", "idle"), lacks("(not committed yet)")))

	a.drag("hello world", 0, 11)
	a.key("ctrl+c")
	s := a.screen()
	r := rows(s)
	if len(r) != 30 {
		t.Errorf("the screen is %d rows, want 30:\n%s", len(r), s)
	}
	if !strings.Contains(r[len(r)-2], "copied") || !strings.Contains(r[len(r)-1], "model scripted") {
		t.Errorf("the note and the line are not the last two rows:\n%s", s)
	}
}
