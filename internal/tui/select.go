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
// A click, a press and a release with no motion between, still acts on
// what is under it (nav.go's click): a row, an item of the tree, a
// place in the input.
// The selection is drawn reversed, like a terminal's own, and stays
// until the next key or press.
//
// The selection is held on the text, not on the screen. An end on the
// viewport is kept as a line of the viewport's content, so when the
// content moves under it (a run streaming at the bottom scrolls the
// conversation up, or the wheel scrolls it) the selection moves with
// the text, and a copy takes the text that was selected, even the part
// scrolled out of sight. An end anywhere else (the status line, the
// panes, the input) is kept as a line of the screen, since nothing
// scrolls there.

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

// selection is a range being copied: a, the cell the button went down
// on, and b, the cell the pointer is on.
type selection struct {
	a, b end
}

// end is one end of a selection: a column and a line. On the viewport
// the line is the line of the viewport's content, so the end stays on its
// text when the content scrolls; elsewhere it is the line of the screen.
type end struct {
	x, line   int
	onContent bool
}

// endAt is the end at screen cell (x, y). The status line is line 0; the
// viewport starts under it.
func (m *Model) endAt(x, y int) end {
	if y >= 1 && y <= m.vp.Height {
		return end{x: x, line: m.vp.YOffset + y - 1, onContent: true}
	}
	return end{x: x, line: y}
}

// screenY is the screen line end e is on now: off the viewport, above or
// below it, when its text is scrolled out of sight.
func (m *Model) screenY(e end) int {
	if e.onContent {
		return e.line - m.vp.YOffset + 1
	}
	return e.line
}

// onContent is whether both ends of the selection are on the viewport's
// content, so the whole of it moves with the text.
func (s *selection) onContent() bool { return s.a.onContent && s.b.onContent }

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
				m.sel = &selection{a: m.endAt(m.pressX, m.pressY)}
			}
			m.sel.b = m.endAt(msg.X, msg.Y)
			return nil
		case tea.MouseActionRelease:
			if !m.pressed {
				break
			}
			m.pressed = false
			if m.sel != nil {
				return nil // a drag, not a click: the selection stays drawn
			}
			return m.click(msg.X, msg.Y)
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
// the layout adds trimmed off the ends. A selection on the viewport's
// content reads the content, all of it, scrolled out of sight or not;
// any other reads the last frame's lines, which is what the pointer was
// over.
func (m *Model) selectedText() string {
	lines := m.lines
	x0, y0, x1, y1 := m.selScreen()
	if m.sel.onContent() {
		lines = m.content
		x0, y0, x1, y1 = selRange(m.sel)
	}
	var rows []string
	for _, r := range region(x0, y0, x1, y1, 0, len(lines)-1) {
		_, mid, _ := cutLine(lines[r.line], r.from, r.to)
		rows = append(rows, strings.TrimRight(ansi.Strip(mid), " "))
	}
	return strings.Join(rows, "\n")
}

// markSelection renders the frame's lines with the selected region
// reversed. A selection on the viewport's content is drawn where its
// text is now, and only on the viewport: the part scrolled out of sight
// is not drawn over the lines around it.
func (m *Model) markSelection(lines []string) []string {
	if m.sel == nil || len(lines) == 0 {
		return lines
	}
	x0, y0, x1, y1 := m.selScreen()
	lo, hi := 0, len(lines)-1
	if m.sel.onContent() {
		lo, hi = 1, min(m.vp.Height, hi)
	}
	out := append([]string(nil), lines...)
	for _, r := range region(x0, y0, x1, y1, lo, hi) {
		before, mid, after := cutLine(out[r.line], r.from, r.to)
		out[r.line] = before + selStyle.Render(ansi.Strip(mid)) + after
	}
	return out
}

// selScreen is the selection on the screen as it is now, in reading
// order.
func (m *Model) selScreen() (x0, y0, x1, y1 int) {
	a, b := m.sel.a, m.sel.b
	a.line, b.line = m.screenY(a), m.screenY(b)
	return ordered(a, b)
}

// selRange is the selection in reading order, in the lines its ends are
// held by.
func selRange(s *selection) (x0, y0, x1, y1 int) { return ordered(s.a, s.b) }

// ordered is two ends in reading order: the topmost first, or the
// leftmost on the same line.
func ordered(a, b end) (x0, y0, x1, y1 int) {
	if b.line < a.line || (b.line == a.line && b.x < a.x) {
		a, b = b, a
	}
	return a.x, a.line, b.x, b.line
}

// lineCut is the part of one line a region covers: columns [from, to).
type lineCut struct{ line, from, to int }

// region is the region from (x0, y0) to (x1, y1), in reading order, cut
// to lines lo..hi: whole lines between the first and the last, columns on
// those two. A first or last line cut off by lo or hi leaves the line at
// the edge whole.
func region(x0, y0, x1, y1, lo, hi int) []lineCut {
	var out []lineCut
	for i := max(y0, lo); i <= min(y1, hi); i++ {
		c := lineCut{line: i, from: 0, to: 1 << 30}
		if i == y0 {
			c.from = x0
		}
		if i == y1 {
			c.to = x1
		}
		out = append(out, c)
	}
	return out
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
