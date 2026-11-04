package tui

import tea "charm.land/bubbletea/v2"

// Reads is how many times the model's inspector read the session, for a
// test that the panes are computed only when asked for.
func (m *Model) Reads() int { return m.insp.Reads() }

// CutLine is select.go's line splitter, for tests.
var CutLine = cutLine

// Selecting is whether a selection is drawn, for tests, which read the
// screen with its styles stripped.
func (m *Model) Selecting() bool { return m.sel != nil }

// IntentDone is the message an intent's command returns, refused with
// err, for a test to play a refusal that lands at a moment of its
// choosing.
func IntentDone(kind string, err error) tea.Msg { return intentDoneMsg{kind: kind, err: err} }
