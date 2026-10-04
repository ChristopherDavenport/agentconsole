package tui_test

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ChristopherDavenport/agentconsole/internal/tui"
)

// The screen's own selection: a left drag marks a range and the release
// copies it to the clipboard. The mouse owns the screen (cell motion
// tracking), so the terminal's own selection cannot reach the client.

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

func TestDraggingCopiesTheSelectedText(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hello world")))
	a.submit("hi")
	a.waitFor("the answer", all(has("hello world", "idle")))

	a.drag("hello world", 0, 11)
	if got := a.copied(); len(got) != 1 || got[0] != "hello world" {
		t.Errorf("copied %q, want [\"hello world\"]", got)
	}
	if !a.m.Selecting() {
		t.Errorf("the selection is not drawn after the release")
	}
}

func TestDraggingCopiesWholeLinesBetween(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hello world")))
	a.submit("hi")
	a.waitFor("the answer", all(has("hello world", "idle")))

	// From the label's line to the text's: whole lines between, columns
	// on the two ends.
	ya, yw := lineOf(a.screen(), "assistant"), lineOf(a.screen(), "hello world")
	a.dragCells(0, ya, 11, yw)
	want := "assistant\nhello world"
	if got := a.copied(); len(got) != 1 || got[0] != want {
		t.Errorf("copied %q, want [%q]", got, want)
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

func TestDraggingTheStatusLineCopiesIt(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hello world")))
	a.submit("hi")
	a.waitFor("the answer", all(has("hello world", "idle")))

	a.dragCells(0, 0, 99, 0)
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
	// A drag over a row's line copies text; it does not put the cursor on
	// the row, which a click would.
	s := a.drag(`"text":"p"`, 0, 6)
	if strings.Contains(s, "▶") {
		t.Errorf("the drag selected a row:\n%s", s)
	}
	if got := a.copied(); len(got) != 1 || got[0] == "" {
		t.Errorf("the drag copied nothing: %q", got)
	}
}

func TestAKeyClearsTheSelection(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hello world")))
	a.submit("hi")
	a.waitFor("the answer", all(has("hello world", "idle")))

	a.drag("hello world", 0, 11)
	a.key(tea.KeyEsc)
	if a.m.Selecting() {
		t.Errorf("the selection is still drawn after a key:\n%s", a.screen())
	}
}

func TestAClickWithoutMotionCopiesNothing(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hello world")))
	a.submit("hi")
	a.waitFor("the answer", all(has("hello world", "idle")))

	a.click("hello world")
	if got := a.copied(); len(got) != 0 {
		t.Errorf("a click copied %q", got)
	}
	if s := a.screen(); !strings.Contains(s, "▶") {
		t.Errorf("the click did not select the row:\n%s", s)
	}
}
