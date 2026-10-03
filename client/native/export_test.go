package native

// FailAfterRebase makes ContinueFrom fail with f's error right after the
// recorder moved, for a test of the undo.
func (b *Backend) FailAfterRebase(f func() error) { b.afterRebase = f }
