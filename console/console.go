// Package console runs the terminal client over a [client.Backend]. It
// is the one call an embedding program makes: build a backend (package
// native for an agentturn.Agent, package kitbackend for an agentkit.Kit),
// then Run.
//
//	be, err := kitbackend.New(kit)
//	if err != nil {
//		return err
//	}
//	defer be.Close()
//	return console.Run(ctx, be)
//
// Run owns the terminal for as long as it runs and does what the
// agentconsole binary does: it follows the backend's record and live
// streams into the view, turns an interrupt signal into the same thing
// ctrl+c is in raw mode (abort a run, quit when idle), and, once the
// program ends, aborts a run that is still going and waits for it to
// write its end before it returns, so the store can be closed behind it.
package console

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ChristopherDavenport/agentconsole/client"
	"github.com/ChristopherDavenport/agentconsole/internal/tui"
)

// DefaultDrainTimeout bounds the wait for an aborted run to write its
// end, unless [WithDrainTimeout] says otherwise.
const DefaultDrainTimeout = 3 * time.Second

type config struct {
	in      io.Reader
	out     io.Writer
	drain   time.Duration
	signals bool
	width   int
	height  int
	warn    func(string)
	cost    client.Cost
}

// Option configures [Run].
type Option func(*config)

// WithInput reads the terminal's input from r instead of the process's
// standard input. A reader that is not a terminal runs the program
// without raw mode, which is how a test drives it.
func WithInput(r io.Reader) Option { return func(c *config) { c.in = r } }

// WithOutput renders to w instead of the process's standard output.
func WithOutput(w io.Writer) Option { return func(c *config) { c.out = w } }

// WithWindowSize tells the program its terminal's size, as the terminal
// would, for a run whose input and output are not a terminal and report
// none (the program draws nothing until it knows its size). A terminal
// reports its own size and overrides it.
func WithWindowSize(width, height int) Option {
	return func(c *config) { c.width, c.height = width, height }
}

// WithDrainTimeout bounds how long Run waits, after the program ends,
// for a run it aborted to write its end. The default is
// [DefaultDrainTimeout]. Run waits twice: once for the abort to land,
// and once more after it cancels the context the run used as the last
// resort.
func WithDrainTimeout(d time.Duration) Option { return func(c *config) { c.drain = d } }

// WithoutSignalHandler leaves SIGINT and SIGTERM to the embedder. By
// default Run turns either into an interrupt of the client (the same as
// ctrl+c); without the handler the embedder's own handling applies, and
// the program ends only when the user quits or ctx is done.
func WithoutSignalHandler() Option { return func(c *config) { c.signals = false } }

// WithWarn sends the one warning Run has, that a run did not end within
// the drain timeout and the session is closed as it is, to fn instead of
// to standard error.
func WithWarn(fn func(string)) Option { return func(c *config) { c.warn = fn } }

// WithCost prices one model call, showing the followed session's cost in
// the session pane. fn returns a call's cost in US dollars, and reports
// false for a model it has no price for.
func WithCost(fn client.Cost) Option { return func(c *config) { c.cost = fn } }

// Run runs the terminal client over be until the user quits, ctx is
// done, or the program fails, and returns the program's error.
//
// The backend's Record must be one the client can follow and read, and
// its Live must subscribe when called (the contract says so):
// Run attaches before it starts the program, so no event of the first
// run is missed. Run does not close anything it did not open. The
// backend, its store and the agent's recorder are the caller's, and
// are safe to close once Run returns: the run it was driving has ended
// or the drain timeout passed.
//
// A canceled ctx ends the program with an error, as bubbletea's
// WithContext does.
func Run(ctx context.Context, be client.Backend, opts ...Option) error {
	cfg := config{drain: DefaultDrainTimeout, signals: true, warn: func(s string) { fmt.Fprintln(os.Stderr, "agentconsole:", s) }}
	for _, o := range opts {
		o(&cfg)
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	m := tui.New(ctx, be, tui.WithCost(cfg.cost))
	// In raw mode ctrl+c is a key. A signal from outside (kill, a parent's
	// ctrl+c) is made the same thing: the program's own handling would
	// end it with an error, without aborting the run.
	popts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(ctx), tea.WithoutSignalHandler()}
	if cfg.in != nil {
		popts = append(popts, tea.WithInput(cfg.in))
	}
	if cfg.out != nil {
		popts = append(popts, tea.WithOutput(cfg.out))
	}
	p := tea.NewProgram(m, popts...)
	if cfg.signals {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(sigs)
		go func() {
			for {
				select {
				case <-sigs:
					p.Send(tui.InterruptMsg{})
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	if cfg.width > 0 && cfg.height > 0 {
		// Send blocks until the program is running.
		go p.Send(tea.WindowSizeMsg{Width: cfg.width, Height: cfg.height})
	}
	wait := tui.Attach(ctx, be, p.Send)
	_, err := p.Run()
	// A run may still be going (a second ctrl+c quits without waiting for
	// an abort to land). Let it write its end before the caller closes
	// the store; the context's cancel is the last resort.
	be.Control().Abort()
	if !m.Drain(cfg.drain) {
		cfg.warn("a run did not end; closing the session as it is")
	}
	stop()
	m.Drain(cfg.drain)
	wait()
	return err
}
