package tui_test

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Clicking into things: a row, an item of the tree, the input. A click
// is a press and a release with no motion between.

// clickCell left-clicks screen cell (x, y).
func (a *app) clickCell(x, y int) {
	a.send(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	a.send(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
}

// clickOn left-clicks the first cell of sub on the screen.
func (a *app) clickOn(sub string) {
	a.t.Helper()
	for y, l := range strings.Split(ansi.Strip(a.screen()), "\n") {
		if i := strings.Index(l, sub); i >= 0 {
			a.clickCell(ansi.StringWidth(l[:i]), y)
			return
		}
	}
	a.t.Fatalf("no line holds %q:\n%s", sub, a.screen())
}

func TestClickingTheInputMovesItsCursor(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hello world")))
	a.typeText("hello world")
	a.clickOn("world")
	a.typeText("X")
	if s := a.screen(); !strings.Contains(s, "> hello Xworld") {
		t.Fatalf("the click did not put the cursor before world:\n%s", s)
	}
	// Past the end of the text, the cursor goes to the end.
	y := lineOf(a.screen(), "> hello Xworld")
	a.clickCell(80, y)
	a.typeText("!")
	if s := a.screen(); !strings.Contains(s, "> hello Xworld!") {
		t.Fatalf("a click past the text did not put the cursor at its end:\n%s", s)
	}
	// On the prompt, the cursor goes to the start.
	a.clickCell(0, y)
	a.typeText("^")
	if s := a.screen(); !strings.Contains(s, "> ^hello Xworld!") {
		t.Fatalf("a click on the prompt did not put the cursor at the start:\n%s", s)
	}
}

func TestClickingAWrappedRowOfTheInput(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hello world")))
	a.resize(24, 30)
	a.typeText("alpha bravo charlie delta echo foxtrot golf")
	for _, w := range []string{"bravo", "delta", "alpha", "golf"} {
		a.clickOn(w)
		a.typeText("X")
		if s := a.screen(); !strings.Contains(s, "X"+w) {
			t.Fatalf("a click on %s did not put the cursor before it:\n%s", w, s)
		}
	}
	// Past the end of a row that wraps, the cursor stops on the row: what
	// is typed lands at the row's end, not at the next row's start.
	y := lineOf(a.screen(), "Xalpha")
	a.clickCell(23, y)
	a.typeText("Z")
	if l := lineWith(a.screen(), "Xalpha"); !strings.Contains(l, "Z") {
		t.Fatalf("a click past a wrapped row's end left it: %q\n%s", l, a.screen())
	}
}

// A value taller than the input's rows scrolls inside it; a click lands on
// the row shown under it.
func TestClickingAScrolledInput(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hello world")))
	a.paste("line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8")
	a.screen()
	// The textarea scrolls against what it last rendered: the key after
	// the paste brings the cursor's row into view.
	a.key(tea.KeyEnd)
	if s := a.screen(); strings.Contains(s, "line1") || !strings.Contains(s, "line8") {
		t.Fatalf("the input did not scroll to its end; the test proves nothing:\n%s", s)
	}
	a.clickOn("line5")
	a.typeText("X")
	if s := a.screen(); !strings.Contains(s, "Xline5") {
		t.Fatalf("the click did not put the cursor before line5:\n%s", s)
	}
	// Scroll it up with the cursor, then click the bottom row shown.
	for range 7 {
		a.key(tea.KeyUp)
	}
	s := a.screen()
	if !strings.Contains(s, "line1") || strings.Contains(s, "line8") {
		t.Fatalf("the input did not scroll to its start:\n%s", s)
	}
	a.clickOn("line4")
	a.typeText("Y")
	if s := a.screen(); !strings.Contains(s, "Yline4") {
		t.Fatalf("the click did not put the cursor before line4:\n%s", s)
	}
}

// Right after a paste the textarea has not scrolled yet: it shows the
// first rows with the cursor below them, until its next Update. The click
// still lands on the row shown under it. The cursor is kept from
// blinking, since a blink's message would be that next Update, at a time
// of its own.
func TestClickingAnInputNotScrolledToItsCursor(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hello world")))
	a.mu.Lock()
	a.m.StillCursor()
	a.mu.Unlock()
	a.paste("line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8")
	if s := a.screen(); !strings.Contains(s, "line1") || strings.Contains(s, "line8") {
		t.Fatalf("the input scrolled after the paste; the test proves nothing:\n%s", s)
	}
	a.clickOn("line3")
	a.typeText("X")
	if s := a.screen(); !strings.Contains(s, "Xline3") {
		t.Fatalf("the click did not put the cursor before line3:\n%s", s)
	}
}

func TestClickingTheInputLeavesTheRows(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hello world")))
	a.submit("hi")
	// Committed: a row still overlay (not committed yet) takes no cursor.
	a.waitFor("the answer", all(has("hello world", "idle"), lacks("(not committed yet)")))
	a.click("hello world")
	a.waitFor("the row selected", has("▶"))
	a.key(tea.KeyTab)
	a.waitFor("the detail pane", has("record detail"))

	a.clickOn("say something")
	s := a.screen()
	if strings.Contains(s, "▶") || strings.Contains(s, "record detail") {
		t.Fatalf("a click on the input kept the row and its pane:\n%s", s)
	}
	a.typeText("abc")
	if s := a.screen(); !strings.Contains(s, "> abc") {
		t.Fatalf("typing after the click did not go to the input:\n%s", s)
	}
}

func TestClickingATreeItemSelectsItAndAgainOpensIt(t *testing.T) {
	a, _ := branched(t)
	a.key(tea.KeyCtrlT)
	a.waitFor("the tree", has("assistant: r2", "assistant: r3"))

	a.clickOn("assistant: r2")
	s := a.screen()
	if !strings.Contains(lineWith(s, "assistant: r2"), "▶") || strings.Contains(s, "VIEWING") {
		t.Fatalf("the click did not just select the branch:\n%s", s)
	}
	a.clickOn("assistant: r2")
	a.waitFor("the branch opened", all(has("VIEWING branch", "two", "r2"), lacks("branches of session")))
}
