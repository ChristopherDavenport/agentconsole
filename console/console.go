// Package console runs the terminal client: a view of an agent's record
// and its events, which sends what the user asks as intents to a
// controller. It is the one call an embedding program makes: give the
// client a [client.Record] of the session, a [client.Source] of the
// agent's events and a [client.Controller], then Run. For an agent driven
// through agentturn's control contract (agentkit's Kit.Control in
// process, agentturn's front/control Dial over a wire), the control is
// the source and [client.Direct] the controller:
//
//	agent := agentturn.New(kit.Config(), kit.AgentOptions()...)
//	defer kit.Attach(agent)()
//	ctl, err := kit.Control(agent)
//	if err != nil {
//		return err
//	}
//	rec, err := client.RecordOf(kit.Recorder())
//	if err != nil {
//		return err
//	}
//	return console.Run(ctx, rec, ctl, client.Direct(ctl))
//
// A product with intents of its own to handle (answering some questions
// by rule, recording an answer as a person's, forwarding to an agent
// elsewhere) writes its own controller, and may hand the rest to Direct.
//
// Run owns the terminal for as long as it runs and does what the
// agentconsole binary does: it follows the record and the source's live
// stream into the view, turns an interrupt signal into the same thing
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

	tea "charm.land/bubbletea/v2"

	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client"
	"github.com/ChristopherDavenport/agentconsole/internal/tui"
	"github.com/ChristopherDavenport/agentconsole/toolview"
	"github.com/ChristopherDavenport/agentconsole/view"
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
	tools   toolview.Renderers
	submit  func(context.Context, Line) (Submit, error)
	past    []string
	record  func(string)
	history bool
	offer   func(context.Context, view.Permission) client.GrantOffer
	comp    func(context.Context, Completing) []Completion
	hidden  bool
	// status is the status line's make-up, when statusSet.
	status    StatusLine
	statusSet bool
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

// WithToolRenderers draws the calls of the tools named in rs with their
// renderers, live and from the record alike; a call to any other tool
// shows its name and its arguments as key=value pairs. A later call
// replaces the set.
func WithToolRenderers(rs toolview.Renderers) Option {
	return func(c *config) { c.tools = rs }
}

// WithGrantOffer lets the permission panel offer "always": fn says, for
// a permission, the rule an approval would grant and whether the host
// can keep it beyond the session (dax predicts it with agentpolicy's
// Engine.Would). With a rule, g approves with a grant of it for the
// session and G, when Saved, with the grant kept; the [client.Answer]
// carries the grant and the rule, and the controller makes the grant
// ([client.Direct] makes none). fn runs off the program's goroutine,
// once per call, under a context that ends after two seconds; until it
// answers with a rule, the panel offers only "once".
func WithGrantOffer(fn func(ctx context.Context, p view.Permission) client.GrantOffer) Option {
	return func(c *config) { c.offer = fn }
}

// Completing is the input as the host is asked to complete it: Before,
// what the input holds up to the cursor, Word its end back to the last
// space or line break, which a completion replaces, and Running, whether
// a run goes.
type Completing struct {
	Before  string
	Word    string
	Running bool
}

// Completion is one completion the host offers: Text, which replaces the
// word, and Help, a line shown beside it (a command's arguments and what
// it does), which may be empty.
type Completion struct {
	Text string
	Help string
}

// WithCompletion completes the input from the host: its slash commands,
// the paths of the workspace its tools act in, wherever that is. As the
// word that ends at the cursor changes, fn is asked for it, off the
// program's goroutine, under a context that ends after a second; an
// answer for a word since changed is dropped. fn is called on every
// keystroke that changes the word, prose included, with no debounce, so
// several calls may be in flight at once: it must be safe for
// concurrent use, and should return at once for a word it does not
// complete. fn decides what a word is
// (the client gives a slash or an @ no meaning) and returns nothing for
// one it does not complete. A list with anything in it opens over the
// input: Up and Down choose, Tab puts the chosen Text in place of the
// word, Esc closes it until the word changes; with no list open Tab
// cycles the pane. What is completed is text in the input: an @path is
// sent as typed, for the host's submit function to read as it chooses,
// and the client reads no file.
func WithCompletion(fn func(ctx context.Context, c Completing) []Completion) Option {
	return func(c *config) { c.comp = fn }
}

// WithHidden lists what the record marks hidden from a person's view
// ([view.WithHidden]): an input the host queued with visible false, over
// the input, and a hidden item in the conversation, each marked hidden
// and never shown as the person's words.
func WithHidden() Option { return func(c *config) { c.hidden = true } }

// Line is a line the user submitted in the client's input.
type Line struct {
	// Text is what was typed, without the space around it.
	Text string
	// Running is whether a run was going when the line was taken up.
	Running bool
}

// Submit is what the client does with a submitted line. Prompt starts a
// run with its items; Steer queues its items into the run that goes, or
// the next one; FollowUp queues its items for when the run would stop,
// or the next one; Reply is shown over the input until the next line is
// submitted, and is not on the record. A reply may come with any one of
// a prompt, a steer and a follow-up; a Submit with more than one is
// refused, and an empty one does nothing.
type Submit struct {
	Prompt   []openresponses.Item
	Steer    []openresponses.Item
	FollowUp []openresponses.Item
	Reply    string
}

// WithSubmit hands each line the user submits to fn, which decides what
// it is: the program running the client owns its input, so a line can be
// a command to that program rather than anything the agent sees. fn runs
// off the program's goroutine, one line at a time in the order they
// were submitted; a line submitted meanwhile waits, shown over the
// input. An error fn returns is shown, and the line is given back to the
// input; so is a prompt while a run goes, which the agent would refuse.
// Without WithSubmit, a line is a prompt when no run goes and a steer
// while one does.
func WithSubmit(fn func(ctx context.Context, l Line) (Submit, error)) Option {
	return func(c *config) { c.submit = fn }
}

// WithHistory gives the input a history: Up on its first line and Down
// on its last walk the lines sent, newest first, starting from past
// (oldest first), and bring back what was being typed. record is called
// with each line as it is sent, slash lines and lines the submit
// function refuses included, so the host keeps it, in a file of its own
// (dax keeps ~/.dax/history); the client keeps no file. A line may hold
// a secret (a slash command's token, a pasted key): what to keep is the
// host's to decide, and record may leave any line out of its file; the
// walk still offers it until the program ends. record runs on
// the program's goroutine and must return promptly; nil records
// nothing. A line from the history is only put in the input: Enter
// sends it.
func WithHistory(past []string, record func(line string)) Option {
	return func(c *config) { c.past, c.record, c.history = past, record, true }
}

// Run runs the terminal client over the session rec follows and the
// events src gives, sending what the user asks to act, until the user
// quits, ctx is done, or the program fails, and returns the program's
// error. ctx is the context of every intent; with [client.Direct] a
// prompt's run is under it, while a run the agent starts for a steer
// (Accept's, for a steer that missed the last drain) takes ctx's values
// without its cancellation and ends only on an abort.
//
// Run refuses a status line [WithStatus] makes up that it cannot draw,
// before it starts anything.
//
// rec must be one the client can follow and read. Run subscribes to src
// before it starts the program, so no event of the first run is missed.
// Run does not close anything it did not open. The agent, its store and
// its recorder are the caller's, and are safe to close once Run returns:
// the run that went has ended, as src reports, or the drain timeout
// passed. At exit Run aborts, waits, cancels ctx and aborts again before
// a last wait. The first abort also stops a run a steer's delivery was
// about to start (agentturn v0.0.39 settles that delivery aborted and
// starts none); the second ends a run started for a steer the agent
// accepted after the first, an intent still in flight as the program
// ended.
//
// A canceled ctx ends the program with an error, as bubbletea's
// WithContext does.
func Run(ctx context.Context, rec client.Record, src client.Source, act client.Controller, opts ...Option) error {
	cfg := config{drain: DefaultDrainTimeout, signals: true, warn: func(s string) { fmt.Fprintln(os.Stderr, "agentconsole:", s) }}
	for _, o := range opts {
		o(&cfg)
	}
	var status tui.StatusConfig
	if cfg.statusSet {
		var err error
		if status, err = statusConfig(cfg.status); err != nil {
			return err
		}
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	// A copied selection goes to the terminal's clipboard (OSC 52), written
	// to the same writer the program renders to: the sequence is a command
	// the terminal executes, not output, so it cannot corrupt a frame.
	out := io.Writer(os.Stdout)
	if cfg.out != nil {
		out = cfg.out
	}
	topts := []tui.Option{tui.WithCost(cfg.cost), tui.WithToolRenderers(cfg.tools), tui.WithCopier(tui.Clipboard(out)), tui.WithStatus(status)}
	if cfg.history {
		topts = append(topts, tui.WithHistory(cfg.past, cfg.record))
	}
	if cfg.offer != nil {
		topts = append(topts, tui.WithGrantOffer(cfg.offer))
	}
	if fn := cfg.comp; fn != nil {
		topts = append(topts, tui.WithCompletion(func(ctx context.Context, c tui.Completing) []tui.Completion {
			got := fn(ctx, Completing(c))
			out := make([]tui.Completion, len(got))
			for i, g := range got {
				out[i] = tui.Completion(g)
			}
			return out
		}))
	}
	if fn := cfg.submit; fn != nil {
		topts = append(topts, tui.WithSubmit(func(ctx context.Context, l tui.Line) (tui.Submission, error) {
			s, err := fn(ctx, Line(l))
			return tui.Submission(s), err
		}))
	}
	m := tui.New(ctx, rec, src, act, topts...)
	// In raw mode ctrl+c is a key. A signal from outside (kill, a parent's
	// ctrl+c) is made the same thing: the program's own handling would
	// end it with an error, without aborting the run.
	popts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithoutSignalHandler()}
	if cfg.in != nil {
		popts = append(popts, tea.WithInput(cfg.in))
	}
	if cfg.out != nil {
		popts = append(popts, tea.WithOutput(cfg.out))
	}
	if cfg.width > 0 && cfg.height > 0 {
		popts = append(popts, tea.WithWindowSize(cfg.width, cfg.height))
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
	if up := cfg.status.Updates; up != nil {
		go func() {
			for {
				select {
				case _, ok := <-up:
					if !ok {
						return
					}
					p.Send(tui.StatusMsg{})
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	var vopts []view.Option
	if cfg.hidden {
		vopts = append(vopts, view.WithHidden())
	}
	wait := tui.Attach(ctx, rec, src, p.Send, vopts...)
	_, err := p.Run()
	// A run may still be going (a second ctrl+c quits without waiting for
	// an abort to land). Let it write its end before the caller closes
	// the store; the context's cancel is the last resort.
	// The abort's refusal has nowhere to be shown now; the drain says
	// whether the run ended.
	_ = act(ctx, client.Abort{})
	if !m.Drain(cfg.drain) {
		cfg.warn("a run did not end; closing the session as it is")
	}
	stop()
	// A run Accept starts is under ctx's values without its
	// cancellation, so stop does not end it. The abort above settles a
	// delivery whose run had not started yet (agentturn#312), but a
	// steer accepted after it, sent by a command still in flight as the
	// program ended, starts a run of its own: abort again.
	_ = act(context.WithoutCancel(ctx), client.Abort{})
	m.Drain(cfg.drain)
	wait()
	return err
}
