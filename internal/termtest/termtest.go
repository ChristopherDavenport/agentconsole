// Package termtest is a terminal for tests that run the program over a
// pipe: it takes what the program writes, as a terminal would, and says
// what the screen shows. The renderer writes only the cells that change
// between frames, so a word streamed over two frames is never whole in
// the bytes written; it is on the screen.
package termtest

import (
	"bytes"
	"io"
	"sync"

	"github.com/charmbracelet/x/vt"
)

// Term is a terminal of a fixed size. Its Write is the program's output.
type Term struct {
	mu  sync.Mutex
	emu *vt.Emulator
	raw bytes.Buffer
}

// New returns a terminal width columns wide and height rows tall. Close
// it when the program is done.
func New(width, height int) *Term {
	t := &Term{emu: vt.NewEmulator(width, height)}
	// The emulator answers the program's queries (its modes, its colors)
	// on a pipe of its own, and blocks the write that asked until they are
	// read. The program is not told: it runs as on a terminal that keeps
	// quiet.
	go func() { _, _ = io.Copy(io.Discard, t.emu) }()
	return t
}

// Write takes the program's output.
func (t *Term) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.raw.Write(p)
	return t.emu.Write(p)
}

// Screen is the text the screen shows, its trailing blanks trimmed.
func (t *Term) Screen() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.emu.String()
}

// Raw is every byte the program wrote, for what is a command to the
// terminal rather than something it shows (the clipboard, a mode).
func (t *Term) Raw() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.raw.String()
}

// Close ends the terminal's answers. It closes the pipe they are written
// to rather than the emulator, whose Close races its Read.
func (t *Term) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c, ok := t.emu.InputPipe().(io.Closer); ok {
		return c.Close()
	}
	return nil
}
