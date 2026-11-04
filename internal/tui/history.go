package tui

import (
	tea "charm.land/bubbletea/v2"
)

// history is what the input was sent, oldest first, for Up and Down to
// walk (WithHistory): the host's lines, then this run's. at is the line
// shown, len(lines) for the draft, which is kept while the walk is away
// from it.
type history struct {
	lines  []string
	at     int
	draft  string
	record func(string)
}

// sent keeps a line sent, hands it to the host, and ends the walk. A
// line the host's submit refuses is kept as well: it was typed.
func (h *history) sent(line string) {
	h.lines = append(h.lines, line)
	h.at, h.draft = len(h.lines), ""
	if h.record != nil {
		h.record(line)
	}
}

// walkHistory is Up on the input's first row and Down on its last: the
// line before or after the one shown, or the draft past the newest. A
// line shown is only put in the input; Enter sends it. It reports false
// for a key it leaves to the input, which moves the cursor.
func (m *Model) walkHistory(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if msg.Mod != 0 || len(m.hist.lines) == 0 {
		return nil, false
	}
	h := &m.hist
	switch msg.Code {
	case tea.KeyUp:
		if !m.onFirstRow() {
			return nil, false
		}
		if h.at == 0 {
			return nil, true
		}
		if h.at == len(h.lines) {
			h.draft = m.in.Value()
		}
		h.at--
		m.in.SetValue(h.lines[h.at])
	case tea.KeyDown:
		if !m.onLastRow() || h.at == len(h.lines) {
			return nil, false
		}
		h.at++
		if h.at == len(h.lines) {
			m.in.SetValue(h.draft)
		} else {
			m.in.SetValue(h.lines[h.at])
			m.in.MoveToBegin()
		}
	default:
		return nil, false
	}
	m.relayout()
	return nil, true
}

// onFirstRow and onLastRow say whether the input's cursor is on its
// first or last row on screen, a wrapped line's rows counted.
func (m *Model) onFirstRow() bool {
	return m.in.Line() == 0 && m.in.LineInfo().RowOffset == 0
}

func (m *Model) onLastRow() bool {
	li := m.in.LineInfo()
	return m.in.Line() == m.in.LineCount()-1 && li.RowOffset >= li.Height-1
}
