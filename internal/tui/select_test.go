package tui_test

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ChristopherDavenport/agentconsole/internal/tui"
)

// The screen's own selection: a left drag marks a range and draws it;
// ctrl+c copies it to the clipboard, as a desktop's copy does, when the
// user chooses. The mouse owns the screen (cell motion tracking), so the
// terminal's own selection cannot reach the client.

func TestCutLineSplitsAtColumns(t *testing.T) {
	for _, tc := range []struct {
		line             string
		from, to         int
		before, mid, far string
	}{
		{"abcdef", 2, 4, "ab", "cd", "ef"},
		{"abcdef", 0, 1 << 30, "", "abcdef", ""},
		{"abcdef", 3, 3, "abc", "", "def"},
		// An escape sequence has no width and stays where it sits.
		{"\x1b[1mab\x1b[0m cd", 1, 3, "\x1b[1ma", "b\x1b[0m ", "cd"},
		// A wide rune is one cell: a cut inside it leaves it whole.
		{"你x", 0, 2, "", "你", "x"},
		{"你x", 1, 2, "你", "", "x"},
		// A combining mark rides with the rune before it.
		{"ae\u0301x", 1, 3, "a", "e\u0301x", ""},
	} {
		before, mid, after := tui.CutLine(tc.line, tc.from, tc.to)
		if before != tc.before || mid != tc.mid || after != tc.far {
			t.Errorf("CutLine(%q, %d, %d) = (%q, %q, %q), want (%q, %q, %q)",
				tc.line, tc.from, tc.to, before, mid, after, tc.before, tc.mid, tc.far)
		}
	}
}

func TestCtrlCCopiesTheSelectedText(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hello world")))
	a.submit("hi")
	a.waitFor("the answer", all(has("hello world", "idle")))

	// The drag draws the selection and copies nothing on its own.
	a.drag("hello world", 0, 11)
	if !a.m.Selecting() {
		t.Fatalf("the selection is not drawn after the release:\n%s", a.screen())
	}
	if got := a.copied(); len(got) != 0 {
		t.Errorf("the drag copied %q by itself", got)
	}
	// Ctrl+c copies and drops the selection, so the next one would be
	// the interrupt again.
	a.key(tea.KeyCtrlC)
	if got := a.copied(); len(got) != 1 || got[0] != "hello world" {
		t.Errorf("copied %q, want [\"hello world\"]", got)
	}
	if a.m.Selecting() {
		t.Errorf("copying kept the selection drawn")
	}
	if !strings.Contains(a.screen(), "copied") {
		t.Errorf("the copy is not noted on the status line:\n%s", a.screen())
	}
	if a.quitted() {
		t.Errorf("ctrl+c with a selection drawn quit instead of copying")
	}
}

// Ctrl+c copies even while a run goes: without that, the copy would be
// unreachable mid-run, since ctrl+c is also the abort.
func TestCtrlCCopiesWhileARunGoes(t *testing.T) {
	g := newGates()
	a := newApp(t, cfgWith(say(g, map[int]string{0: "hold"}, "hel", "lo ", "world")))
	t.Cleanup(func() { g.release("hold") })

	a.submit("hi")
	g.arrive(t, "hold")
	a.waitFor("the first chunk streaming", all(has("hel"+cursor, "running")))
	a.drag("hel", 0, 3)
	a.key(tea.KeyCtrlC)
	if got := a.copied(); len(got) != 1 || got[0] != "hel" {
		t.Errorf("copied %q, want [\"hel\"]", got)
	}
	if s := a.screen(); strings.Contains(s, "aborting") || !strings.Contains(s, "running") {
		t.Errorf("ctrl+c aborted the run instead of copying:\n%s", s)
	}
	if a.quitted() {
		t.Errorf("ctrl+c with a selection drawn quit instead of copying")
	}
	// The run goes on to the end, un-aborted.
	g.release("hold")
	a.waitFor("the answer committed", all(has("hello world", "idle"), lacks("aborting")))
}

func TestCtrlCCopiesWholeLinesBetween(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hello world")))
	a.submit("hi")
	// Committed, so the label is the committed one.
	a.waitFor("the answer", all(has("hello world", "idle"), lacks("(not committed yet)")))

	// From the label's line to the text's: whole lines between, columns
	// on the two ends.
	ya, yw := lineOf(a.screen(), "assistant"), lineOf(a.screen(), "hello world")
	a.dragCells(0, ya, 11, yw)
	a.key(tea.KeyCtrlC)
	want := "assistant\nhello world"
	if got := a.copied(); len(got) != 1 || got[0] != want {
		t.Errorf("copied %q, want [%q]", got, want)
	}
}

func TestCtrlCCopiesTheStatusLine(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hello world")))
	a.submit("hi")
	a.waitFor("the answer", all(has("hello world", "idle")))

	a.dragCells(0, 0, 99, 0)
	a.key(tea.KeyCtrlC)
	got := a.copied()
	if len(got) != 1 {
		t.Fatalf("copied %q, want one selection", got)
	}
	if !strings.Contains(got[0], "idle") || !strings.Contains(got[0], "session ") {
		t.Errorf("the status line's text was not copied: %q", got[0])
	}
	if strings.TrimRight(got[0], " ") != got[0] {
		t.Errorf("the status line's padding was copied: %q", got[0])
	}
}

func TestADragDoesNotSelectARow(t *testing.T) {
	a := twoCallsApp(t)
	// A drag over a row's line draws a selection; it does not put the
	// cursor on the row, which a click would.
	s := a.drag(`text="p"`, 0, 6)
	if strings.Contains(s, "▶") {
		t.Errorf("the drag selected a row:\n%s", s)
	}
	a.key(tea.KeyCtrlC)
	if got := a.copied(); len(got) != 1 || got[0] == "" {
		t.Errorf("ctrl+c copied nothing: %q", got)
	}
}

func TestAKeyButCtrlCClearsTheSelection(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hello world")))
	a.submit("hi")
	a.waitFor("the answer", all(has("hello world", "idle")))

	a.drag("hello world", 0, 11)
	a.key(tea.KeyEsc)
	if a.m.Selecting() {
		t.Errorf("the selection is still drawn after a key:\n%s", a.screen())
	}
	if got := a.copied(); len(got) != 0 {
		t.Errorf("a key that clears the selection copied %q", got)
	}
}

func TestAClickWithoutMotionSelectsTheRowAndCopiesNothing(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hello world")))
	a.submit("hi")
	// Committed: a row still overlay (not committed yet) takes no cursor.
	a.waitFor("the answer", all(has("hello world", "idle"), lacks("(not committed yet)")))

	a.click("hello world")
	if got := a.copied(); len(got) != 0 {
		t.Errorf("a click copied %q", got)
	}
	if s := a.screen(); !strings.Contains(s, "▶") {
		t.Errorf("the click did not select the row:\n%s", s)
	}
}

// With nothing selected, ctrl+c is the interrupt it always was.
func TestCtrlCWithoutASelectionQuits(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hello world")))
	a.submit("hi")
	a.waitFor("the answer", all(has("hello world", "idle")))

	a.key(tea.KeyCtrlC)
	a.waitQuit()
	if got := a.copied(); len(got) != 0 {
		t.Errorf("ctrl+c with nothing selected copied %q", got)
	}
}

// The selection is held on the text, not on the screen: a run streaming
// below it scrolls the conversation up, and the selection goes with the
// text, so ctrl+c copies what was selected, even scrolled out of sight.
func TestTheSelectionStaysOnItsTextWhileARunStreams(t *testing.T) {
	g := newGates()
	more := ""
	for i := 1; i <= 40; i++ {
		more += fmt.Sprintf("\n\nline %d", i)
	}
	a := newApp(t, cfgWith(say(g, map[int]string{0: "hold"}, "first words", more)))
	t.Cleanup(func() { g.release("hold") })

	a.submit("hi")
	g.arrive(t, "hold")
	// The prompt's row drawn above it first: a row inserted above a
	// selection moves its text but not the selection (see the plan).
	a.waitFor("the first chunk streaming", all(has("first words"+cursor, "running"), promptRow("hi")))
	y := lineOf(a.screen(), "first words")
	a.drag("first words", 0, 11)

	g.release("hold")
	s := a.waitFor("the answer committed", all(has("line 40", "idle")))
	if lineOf(s, "first words") == y {
		t.Fatalf("the text did not move; the test proves nothing:\n%s", s)
	}
	if !a.m.Selecting() {
		t.Fatalf("the selection was dropped by the run:\n%s", s)
	}
	a.key(tea.KeyCtrlC)
	if got := a.copied(); len(got) != 1 || got[0] != "first words" {
		t.Errorf("copied %q, want [\"first words\"]", got)
	}
}

// The wheel scrolls the text under the selection, and the selection with
// it.
func TestTheSelectionStaysOnItsTextWhenScrolled(t *testing.T) {
	more := ""
	for i := 1; i <= 40; i++ {
		more += fmt.Sprintf("\n\nline %d", i)
	}
	a := newApp(t, cfgWith(say(nil, nil, "first words", more)))
	a.submit("hi")
	a.waitFor("the answer", all(has("line 40", "idle"), lacks("(not committed yet)")))

	a.drag("line 40", 0, 7)
	for range 5 {
		a.send(tea.MouseMsg{X: 0, Y: 5, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	}
	if s := a.screen(); strings.Contains(s, "line 40") {
		t.Fatalf("the wheel did not scroll the text away; the test proves nothing:\n%s", s)
	}
	a.key(tea.KeyCtrlC)
	if got := a.copied(); len(got) != 1 || got[0] != "line 40" {
		t.Errorf("copied %q, want [\"line 40\"]", got)
	}
}

// promptRow is whether the screen shows a line that is text alone, the
// user's prompt under its label.
func promptRow(text string) func(screen string) bool {
	return func(screen string) bool {
		for _, l := range strings.Split(screen, "\n") {
			if strings.TrimSpace(l) == text {
				return true
			}
		}
		return false
	}
}

// lineOf is the screen line holding sub.
func lineOf(screen, sub string) int {
	for y, l := range strings.Split(screen, "\n") {
		if strings.Contains(l, sub) {
			return y
		}
	}
	return -1
}
