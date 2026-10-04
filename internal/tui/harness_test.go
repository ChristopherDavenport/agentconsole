package tui_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentsession/jsonl"
	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client/native"
	"github.com/ChristopherDavenport/agentconsole/internal/tui"
)

const wait = 10 * time.Second

// gates holds a scripted model or tool at a named point until the test
// lets it go, so the screen can be read between two steps of a run.
type gates struct {
	mu      sync.Mutex
	reached map[string]chan struct{}
	open    map[string]chan struct{}
}

func newGates() *gates {
	return &gates{reached: map[string]chan struct{}{}, open: map[string]chan struct{}{}}
}

func (g *gates) chans(name string) (reached, open chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.reached[name] == nil {
		g.reached[name] = make(chan struct{})
		g.open[name] = make(chan struct{})
	}
	return g.reached[name], g.open[name]
}

func (g *gates) hold(ctx context.Context, name string) error {
	reached, open := g.chans(name)
	close(reached)
	select {
	case <-open:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *gates) arrive(t *testing.T, name string) {
	t.Helper()
	reached, _ := g.chans(name)
	select {
	case <-reached:
	case <-time.After(wait):
		t.Fatalf("script never reached %q", name)
	}
}

func (g *gates) release(name string) {
	_, open := g.chans(name)
	g.mu.Lock()
	defer g.mu.Unlock()
	select {
	case <-open:
	default:
		close(open)
	}
}

type script struct {
	mu        sync.Mutex
	responses []func(ctx context.Context, em *openresponses.Emitter) error
	n         int
}

func (s *script) CreateStream(ctx context.Context, req openresponses.Request, sink openresponses.EventSink) error {
	s.mu.Lock()
	i := s.n
	s.n++
	s.mu.Unlock()
	if i >= len(s.responses) {
		return fmt.Errorf("script: request %d, only %d responses", i+1, len(s.responses))
	}
	em := openresponses.NewEmitter(sink, openresponses.NewResponse(req))
	if err := s.responses[i](ctx, em); err != nil {
		return err
	}
	return em.Complete()
}

type step = func(ctx context.Context, em *openresponses.Emitter) error

func say(g *gates, holds map[int]string, chunks ...string) step {
	return func(ctx context.Context, em *openresponses.Emitter) error {
		w, err := em.Message(openresponses.PhaseFinalAnswer)
		if err != nil {
			return err
		}
		for i, c := range chunks {
			if err := w.Text(c); err != nil {
				return err
			}
			if name, ok := holds[i]; ok {
				if err := g.hold(ctx, name); err != nil {
					return err
				}
			}
		}
		return w.Close()
	}
}

func usageSay(in, out int, text string) step {
	return func(_ context.Context, em *openresponses.Emitter) error {
		em.Response().Usage = &openresponses.Usage{
			InputTokens:  in,
			OutputTokens: out,
			TotalTokens:  in + out,
		}
		w, err := em.Message(openresponses.PhaseFinalAnswer)
		if err != nil {
			return err
		}
		if err := w.Text(text); err != nil {
			return err
		}
		return w.Close()
	}
}

func think(text string, then step) step {
	return func(ctx context.Context, em *openresponses.Emitter) error {
		w, err := em.Reasoning()
		if err != nil {
			return err
		}
		if err := w.Text(text); err != nil {
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
		return then(ctx, em)
	}
}

func callTool(callID, name, args string) step {
	return func(_ context.Context, em *openresponses.Emitter) error {
		w, err := em.FunctionCall(callID, name)
		if err != nil {
			return err
		}
		if err := w.Arguments(args); err != nil {
			return err
		}
		return w.Close()
	}
}

type textArgs struct {
	Text string `json:"text"`
}

func upperTool(g *gates) agenttool.Tool {
	return agenttool.New("upper", "uppercase", func(ctx context.Context, a textArgs) (string, error) {
		if g != nil {
			agenttool.Progress(ctx, agenttool.Text("working"))
			if err := g.hold(ctx, "tool"); err != nil {
				return "", err
			}
		}
		return strings.ToUpper(a.Text), nil
	})
}

func deferAll(reason string) func(context.Context, agentturn.ToolCallInfo) (*agentturn.ToolDecision, error) {
	return func(context.Context, agentturn.ToolCallInfo) (*agentturn.ToolDecision, error) {
		return &agentturn.ToolDecision{Action: agentturn.Defer, Reason: reason}, nil
	}
}

// app is a real agent, recorder, native backend and TUI model. It stands
// in for a tea.Program: messages are applied to the model one at a time,
// and the commands the model returns run on goroutines whose results come
// back as messages, as the program does.
type app struct {
	be     *native.Backend
	t      *testing.T
	ctx    context.Context
	cancel context.CancelFunc

	mu   sync.Mutex
	m    *tui.Model
	quit bool

	wg   sync.WaitGroup
	wait func()

	// feedDelay holds each model the feed sends, to play a slow terminal.
	feedDelay atomic.Int64

	store agentsession.Store
	rec   *session.Recorder
}

// starter opens the session the app's recorder writes, on the store.
type starter func(ctx context.Context, store agentsession.Store) (*session.Recorder, error)

func newApp(t *testing.T, cfg agentturn.Config) *app { return newAppOn(t, cfg, nil) }

// newAppOn is newApp with a session of the test's own making, a fork for
// one.
func newAppOn(t *testing.T, cfg agentturn.Config, start starter) *app {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	store, err := jsonl.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var rec *session.Recorder
	if start != nil {
		rec, err = start(ctx, store)
	} else {
		rec, _, err = session.Start(ctx, store, agentsession.Header{Records: agentsession.AllRecords,
			Harness: &agentsession.Harness{Name: "tui-test", Version: "9"}, CWD: "/work/dir"})
	}
	if err != nil {
		t.Fatal(err)
	}
	ag := agentturn.New(cfg)
	detach := rec.Attach(ag)
	be, err := native.New(ag, rec)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{t: t, ctx: ctx, cancel: cancel, m: tui.New(ctx, be), store: store, rec: rec, be: be}
	a.wait = tui.Attach(ctx, be, func(msg tea.Msg) {
		if _, ok := msg.(tui.ModelMsg); ok {
			time.Sleep(time.Duration(a.feedDelay.Load()))
		}
		a.send(msg)
	})
	t.Cleanup(func() {
		cancel()
		a.wait()
		a.wg.Wait()
		detach()
		if err := store.Close(); err != nil {
			t.Errorf("close the store: %v", err)
		}
	})
	a.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	return a
}

// send applies a message, then runs the command it returned.
func (a *app) send(msg tea.Msg) {
	a.mu.Lock()
	if _, ok := msg.(tea.QuitMsg); ok {
		a.quit = true
	}
	_, cmd := a.m.Update(msg)
	a.mu.Unlock()
	a.run(cmd)
}

func (a *app) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		switch msg := cmd().(type) {
		case nil:
		case tea.BatchMsg:
			for _, c := range msg {
				a.run(c)
			}
		default:
			if a.ctx.Err() == nil {
				a.send(msg)
			}
		}
	}()
}

func (a *app) screen() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.m.View()
}

func (a *app) waitQuit() {
	a.t.Helper()
	deadline := time.Now().Add(wait)
	for !a.quitted() {
		if time.Now().After(deadline) {
			a.t.Fatal("the program did not quit")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (a *app) quitted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.quit
}

// paste sends value as the terminal sends a paste: one key message
// holding every rune, over bracketed paste, that the model sees whole.
func (a *app) paste(value string) {
	a.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value), Paste: true})
}

// typeText types text, one key per rune.
func (a *app) typeText(text string) {
	for _, r := range text {
		a.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func (a *app) key(t tea.KeyType) { a.send(tea.KeyMsg{Type: t}) }

// submit types a line and presses Enter.
func (a *app) submit(text string) {
	a.typeText(text)
	a.key(tea.KeyEnter)
}

// waitFor blocks until the screen satisfies cond, and returns it.
func (a *app) waitFor(what string, cond func(screen string) bool) string {
	a.t.Helper()
	deadline := time.Now().Add(wait)
	for {
		s := a.screen()
		if cond(s) {
			return s
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("timed out waiting for %s; the screen is:\n%s", what, s)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func has(subs ...string) func(string) bool {
	return func(s string) bool {
		for _, sub := range subs {
			if !strings.Contains(s, sub) {
				return false
			}
		}
		return true
	}
}

func lacks(subs ...string) func(string) bool {
	return func(s string) bool {
		for _, sub := range subs {
			if strings.Contains(s, sub) {
				return false
			}
		}
		return true
	}
}

func all(cs ...func(string) bool) func(string) bool {
	return func(s string) bool {
		for _, c := range cs {
			if !c(s) {
				return false
			}
		}
		return true
	}
}

func cfgWith(m ...step) agentturn.Config {
	return agentturn.Config{ModelName: "scripted", Model: &script{responses: m}}
}
