package tui

// Reads is how many times the model's inspector read the session, for a
// test that the panes are computed only when asked for.
func (m *Model) Reads() int { return m.insp.Reads() }

// PasteForTest replaces the input with value, as a pasted prompt would,
// and lays the screen out again.
func (m *Model) PasteForTest(value string) {
	m.in.SetValue(value)
	m.relayout()
}
