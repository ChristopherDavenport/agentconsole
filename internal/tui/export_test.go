package tui

import tacursor "github.com/charmbracelet/bubbles/cursor"

// Reads is how many times the model's inspector read the session, for a
// test that the panes are computed only when asked for.
func (m *Model) Reads() int { return m.insp.Reads() }

// CutLine is select.go's line splitter, for tests.
var CutLine = cutLine

// Selecting is whether a selection is drawn, for tests: lipgloss drops
// the reverse video when the profile has no color, so the drawing itself
// cannot be asserted.
func (m *Model) Selecting() bool { return m.sel != nil }

// StillCursor stops the input's cursor blinking, for a test that needs
// the textarea left alone after a key: a blink, or the end of one a
// relayout cancels, comes back as a message on its own goroutine and
// runs the textarea's Update, which moves its scroll.
func (m *Model) StillCursor() { m.in.Cursor.SetMode(tacursor.CursorStatic) }
