package tui

// Selecting and copying text.
//
// The client takes the whole screen and has the mouse (cell motion
// tracking, for the wheel and the row click), so the terminal's own
// selection cannot reach it: a drag is reported to the program, not to
// the terminal, and which modifier bypasses mouse reporting is the
// terminal's choice, not the client's. So the client selects itself. A
// left drag marks a range of the screen and draws it; copying is a
// choice, not a side effect of the drag: ctrl+c with a selection drawn
// sends the selection's plain text to the terminal's clipboard with
// OSC 52, the one channel a program in the alternate screen has to the
// clipboard (it works over ssh too), as a desktop's copy does — and
// drops the selection, so the next ctrl+c is the interrupt again.
//
// A click, a press and a release with no motion between, still selects
// the row under it (a second click on that row expands it), as it did.
// The selection is drawn reversed, like a terminal's own, and stays
// until the next key or press.

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// selStyle marks the selected region, the way a terminal reverses its
// own selection.
var selStyle = lipgloss.NewStyle().Reverse(true)

// selection is a range of the screen being copied: the cell the button
// went down on and the cell the pointer is on.
type selection struct {
	ax, ay, x, y int
}

// Clipboard is a copier that puts text on the terminal's clipboard with
// OSC 52, writing to the writer the program renders to. The sequence is
// a command the terminal executes, not output: it takes no space on the
// screen, so writing it beside a frame cannot corrupt the frame.
func Clipboard(w io.Writer) func(string) {
	return func(text string) {
		fmt.Fprint(w, ansi.SetSystemClipboard(text))
	}
}

// WithCopier sets where a copied selection goes. Without one a selection
// is still drawn, but nothing is copied.
func WithCopier(fn func(string)) Option { return func(m *Model) { m.copier = fn } }

// mouse is every mouse event. A left press anchors both a click and a
// selection; motion with the button down selects, and the release ends
// the drag, leaving the selection drawn — or, when no motion came
// between, clicks the row under it. Anything else goes to the viewport,
// which scrolls.
func (m *Model) mouse(msg tea.MouseMsg) tea.Cmd {
	if msg.Button == tea.MouseButtonLeft {
		switch msg.Action {
		case tea.MouseActionPress:
			m.pressed, m.pressX, m.pressY = true, msg.X, msg.Y
			m.sel = nil
			return nil
		case tea.MouseActionMotion:
			if !m.pressed {
				break
			}
			if m.sel == nil {
				if msg.X == m.pressX && msg.Y == m.pressY {
					return nil
				}
				m.sel = &selection{ax: m.pressX, ay: m.pressY}
			}
			m.sel.x, m.sel.y = msg.X, msg.Y
			return nil
		case tea.MouseActionRelease:
			if !m.pressed {
				break
			}
			m.pressed = false
			if m.sel != nil {
				return nil // a drag, not a click: the selection stays drawn
			}
			return m.click(msg.Y)
		}
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return cmd
}

// clearSelection drops a drawn selection: any key but ctrl+y does, as a
// terminal's own selection goes on typing.
func (m *Model) clearSelection() { m.sel = nil }

// copySelection is ctrl+c with a selection drawn: it copies the
// selected region's plain text and notes it on the status line. The
// lines are the last frame's, which is what the pointer was over. The
// caller drops the selection.
func (m *Model) copySelection() {
	if m.copier == nil {
		return
	}
	m.copier(m.selectedText())
	m.note = "copied"
}

// selectedText is the plain text of the selected region: whole lines
// between the first and the last, columns on those two, with the padding
// the layout adds trimmed off the ends.
func (m *Model) selectedText() string {
	if len(m.lines) == 0 {
		return ""
	}
	x0, y0, x1, y1 := selRange(m.sel)
	y0, y1 = max(y0, 0), min(y1, len(m.lines)-1)
	var rows []string
	for i := y0; i <= y1; i++ {
		from, to := 0, 1<<30
		if i == y0 {
			from = x0
		}
		if i == y1 {
			to = x1
		}
		_, mid, _ := cutLine(m.lines[i], from, to)
		rows = append(rows, strings.TrimRight(ansi.Strip(mid), " "))
	}
	return strings.Join(rows, "\n")
}

// markSelection renders the frame's lines with the selected region
// reversed. The region is in reading order: whole lines between the
// first and the last, columns on those two.
func markSelection(lines []string, sel *selection) []string {
	if sel == nil || len(lines) == 0 {
		return lines
	}
	x0, y0, x1, y1 := selRange(sel)
	out := append([]string(nil), lines...)
	y0, y1 = max(y0, 0), min(y1, len(out)-1)
	for i := y0; i <= y1; i++ {
		from, to := 0, 1<<30
		if i == y0 {
			from = x0
		}
		if i == y1 {
			to = x1
		}
		before, mid, after := cutLine(out[i], from, to)
		out[i] = before + selStyle.Render(ansi.Strip(mid)) + after
	}
	return out
}

// selRange is the selection in reading order: the topmost cell first, or
// the leftmost on the same line.
func selRange(s *selection) (x0, y0, x1, y1 int) {
	if s.y < s.ay || (s.y == s.ay && s.x < s.ax) {
		return s.x, s.y, s.ax, s.ay
	}
	return s.ax, s.ay, s.x, s.y
}

// cutLine splits a rendered line at display columns [from, to): what
// sits before from, what sits in [from, to), and what sits from to on.
// Escape sequences carry no width and stay with the text they sit in.
func cutLine(line string, from, to int) (before, mid, after string) {
	var b, m, a strings.Builder
	cur := &b
	at := 0 // the column the next rune starts at
	for i := 0; i < len(line); {
		if line[i] == 0x1b {
			j := seqEnd(line, i)
			cur.WriteString(line[i:j])
			i = j
			continue
		}
		r, w := utf8.DecodeRuneInString(line[i:])
		i += w
		rw := runewidth.RuneWidth(r)
		switch {
		case rw == 0: // a combining mark rides with the rune before it
		case at >= to:
			cur = &a
		case at >= from:
			cur = &m
		default:
			cur = &b
		}
		cur.WriteRune(r)
		at += rw
	}
	return b.String(), m.String(), a.String()
}

// seqEnd is where the escape sequence starting at i ends, one past its
// last byte.
func seqEnd(s string, i int) int {
	if i+1 >= len(s) {
		return i + 1
	}
	switch s[i+1] {
	case '[': // CSI: parameters and intermediates, then a final byte
		for j := i + 2; j < len(s); j++ {
			if s[j] >= 0x40 && s[j] <= 0x7e {
				return j + 1
			}
		}
		return len(s)
	case ']': // OSC: BEL, or ST (esc \)
		for j := i + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
		}
		return len(s)
	default: // a two-byte escape, esc 7 and the like
		return i + 2
	}
}
