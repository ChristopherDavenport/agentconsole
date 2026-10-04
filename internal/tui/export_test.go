package tui

// Reads is how many times the model's inspector read the session, for a
// test that the panes are computed only when asked for.
func (m *Model) Reads() int { return m.insp.Reads() }
