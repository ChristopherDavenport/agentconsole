package tui

// Clicking into the input.
//
// A click on the input leaves the rows for the input, as Esc does, and
// puts the input's cursor on the cell clicked: on the rune under the
// pointer, or at the end of the row when the click is past its text.
//
// The textarea keeps its own scroll, for a value taller than the rows it
// shows, and does not tell where it is; nor can it be worked out from
// the keys, since the textarea clamps its scroll against the content it
// last rendered, and when it renders is the program's frame rate. So the
// click reads it from the frame it was made on: the scroll is where the
// value's rows line up with the input's rows on the screen. A click
// moves the cursor only to a row shown, so the scroll stays where it is.

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// inputRows is the value's rows as the textarea wraps them, each with the
// line of the value it is part of.
func (m *Model) inputRows() (rows []string, lineOf []int) {
	for i, l := range strings.Split(m.in.Value(), "\n") {
		for _, r := range wrapLine([]rune(l), m.in.Width()) {
			rows = append(rows, string(r))
			lineOf = append(lineOf, i)
		}
	}
	return rows, lineOf
}

// inputCursorRow is the row of the value the cursor is on, counting the
// soft-wrapped rows of every line above it.
func (m *Model) inputCursorRow() int {
	lines := strings.Split(m.in.Value(), "\n")
	row := 0
	for i := 0; i < m.in.Line() && i < len(lines); i++ {
		row += wrapRows([]rune(lines[i]), m.in.Width())
	}
	return row + m.in.LineInfo().RowOffset
}

// inputScroll is the first of the value's rows the input showed on the
// last frame: the one from which the value's rows read as the frame's
// input rows do. Of two that both do (rows that repeat), the one that
// shows the cursor's row, as the textarea keeps it in view.
func (m *Model) inputScroll(rows []string) int {
	var shown []string
	pw := lipgloss.Width(m.in.Prompt)
	for y := m.inputY; y < m.inputY+m.in.Height() && y < len(m.lines); y++ {
		_, text, _ := cutLine(ansi.Strip(m.lines[y]), pw, 1<<30)
		shown = append(shown, strings.TrimSpace(text))
	}
	cur, found := m.inputCursorRow(), -1
	for top := range rows {
		match := true
		for i, s := range shown {
			r := ""
			if top+i < len(rows) {
				r = strings.TrimSpace(rows[top+i])
			}
			if r != s {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		if cur >= top && cur < top+len(shown) {
			return top
		}
		if found < 0 {
			found = top
		}
	}
	return max(found, 0)
}

// clickInput is a click on cell (x, y) of the input, y counted from its
// first row shown. On a permission or a question waiting for y or n the
// input is off, and the click only leaves the rows.
func (m *Model) clickInput(x, y int) {
	if m.curEntry != "" {
		m.curEntry, m.pane, m.panes = "", paneNone, paneState{}
		defer m.relayout()
	}
	if !m.in.Focused() || m.in.Value() == "" {
		return
	}
	rows, _ := m.inputRows()
	target := min(m.inputScroll(rows)+y, len(rows)-1)
	for cur := m.inputCursorRow(); cur < target; cur++ {
		m.in.CursorDown()
	}
	for cur := m.inputCursorRow(); cur > target; cur-- {
		m.in.CursorUp()
	}
	// The column: the rune under x, past the prompt, on this soft-wrapped
	// row. A row that wraps ends in the space it wrapped at, and a cursor
	// past that space is on the next row, so the click stops on it.
	li := m.in.LineInfo()
	line := []rune(strings.Split(m.in.Value(), "\n")[m.in.Line()])
	col, end := li.StartColumn, min(li.StartColumn+li.Width, len(line))
	if li.RowOffset < li.Height-1 {
		end--
	}
	dx := x - lipgloss.Width(m.in.Prompt)
	for at := 0; col < end; col++ {
		w := runewidth.RuneWidth(line[col])
		if at+w > dx {
			break
		}
		at += w
	}
	m.in.SetCursor(col)
}
