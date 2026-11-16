package tui

// Clicking into the input.
//
// A click on the input leaves the rows for the input, as Esc does, and
// puts the input's cursor on the cell clicked: on the rune under the
// pointer, or at the end of the row when the click is past its text.
//
// The textarea keeps its own scroll, for a value taller than the rows it
// shows. It follows the cursor on every Update and is clamped to the
// value when the textarea is drawn, which the program does after every
// Update, so at a click it is the scroll of the frame clicked on. A click
// moves the cursor only to a row shown, so the scroll stays where it is.

import (
	"strings"

	"charm.land/lipgloss/v2"
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
	target := min(m.in.ScrollYOffset()+y, len(rows)-1)
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
	m.in.SetCursorColumn(col)
}

// noBidi is s less the bidirectional formatting characters view.Clean
// removes (U+061C, U+200E-U+200F, U+202A-U+202E, U+2066-U+2069). Text put
// in the input goes through the textarea's own sanitizer, which drops the
// C0 and C1 controls but keeps these, so a key, a paste, a host's
// completion or a line from the host's history could put a right-to-left
// override in the input line, where it makes what is about to be sent
// read as something else. Every other character is kept, of any script.
// TestNoBidiDropsWhatCleanDrops holds the set to view.Clean's.
func noBidi(s string) string {
	if strings.IndexFunc(s, isBidi) < 0 {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isBidi(r) {
			return -1
		}
		return r
	}, s)
}

func isBidi(r rune) bool {
	return r == 0x061c || r == 0x200e || r == 0x200f || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069
}

// dropBidi takes the bidi controls out of the input after the textarea
// has changed it, and puts the cursor back where the person's text puts
// it: on the same line, less the controls that were before it. It is the
// one place every message the textarea takes (a typed key, a paste, the
// textarea's own ctrl+v paste of the clipboard) is filtered, whatever
// the message's type.
func (m *Model) dropBidi() {
	v := m.in.Value()
	if strings.IndexFunc(v, isBidi) < 0 {
		return
	}
	row, col := m.in.Line(), m.in.Column()
	if lines := strings.Split(v, "\n"); row < len(lines) {
		line := []rune(lines[row])
		col -= len(line[:min(col, len(line))]) - len([]rune(noBidi(string(line[:min(col, len(line))]))))
	}
	m.in.SetValue(noBidi(v))
	for m.in.Line() > row {
		m.in.CursorUp()
	}
	m.in.SetCursorColumn(col)
}

// setInput is the textarea's SetValue less the bidi controls: every value
// the model sets comes through here, as every message comes through
// dropBidi.
func (m *Model) setInput(s string) { m.in.SetValue(noBidi(s)) }
