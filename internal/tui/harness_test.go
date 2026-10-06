package tui_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentsession/jsonl"
	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client"
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

	// copies are the selections the model copied, in order, under copyMu:
	// the model copies from Update, which runs under mu, so the copies
	// have a lock of their own.
	copyMu sync.Mutex
	copies []string
}

// starter opens the session the app's recorder writes, on the store.
type starter func(ctx context.Context, store agentsession.Store) (*session.Recorder, error)

func newApp(t *testing.T, cfg agentturn.Config) *app { return newAppOn(t, cfg, nil) }

// newAppOn is newApp with a session of the test's own making, a fork for
// one.
func newAppOn(t *testing.T, cfg agentturn.Config, start starter) *app {
	t.Helper()
	return newAppWith(t, cfg, start, nil)
}

// newAppWith is newAppOn with the backend the model drives wrapped, to
// play a backend that fails where the native one does not.
func newAppWith(t *testing.T, cfg agentturn.Config, start starter, wrap func(client.Backend) client.Backend) *app {
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
	a := &app{t: t, ctx: ctx, cancel: cancel, store: store, rec: rec, be: be}
	var driven client.Backend = be
	if wrap != nil {
		driven = wrap(be)
	}
	a.m = tui.New(ctx, driven, tui.WithCopier(func(text string) {
		a.copyMu.Lock()
		defer a.copyMu.Unlock()
		a.copies = append(a.copies, text)
	}))
	a.wait = tui.Attach(ctx, driven, func(msg tea.Msg) {
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

// screen is the screen as text. lipgloss styles it whatever the
// terminal, and the program fits the colors to the terminal it writes to,
// so the styles are stripped.
func (a *app) screen() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return ansi.Strip(a.m.View().Content)
}

// copied is what the model copied to the clipboard, in order.
func (a *app) copied() []string {
	a.copyMu.Lock()
	defer a.copyMu.Unlock()
	return append([]string(nil), a.copies...)
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

// paste sends value as the terminal sends a paste: one message holding
// every rune, over bracketed paste, that the model sees whole.
func (a *app) paste(value string) { a.send(tea.PasteMsg{Content: value}) }

// typeText types text, one key per rune.
func (a *app) typeText(text string) {
	for _, r := range text {
		a.send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// key presses the key bubbletea names name ("enter", "ctrl+p", "f1").
func (a *app) key(name string) {
	a.t.Helper()
	a.send(keyPress(a.t, name))
}

// keyNames are the keys with a name of their own that the tests press.
var keyNames = map[string]rune{
	"enter": tea.KeyEnter, "tab": tea.KeyTab, "esc": tea.KeyEscape,
	"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
	"home": tea.KeyHome, "end": tea.KeyEnd, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown,
	"backspace": tea.KeyBackspace, "f1": tea.KeyF1,
}

// keyPress is the press of the key bubbletea names name: modifiers
// joined to a key's name or its rune with "+". It fails the test when the
// message does not name itself so, which would make it a different key.
func keyPress(t *testing.T, name string) tea.KeyPressMsg {
	t.Helper()
	parts := strings.Split(name, "+")
	base := parts[len(parts)-1]
	if base == "" { // "ctrl++"
		base = "+"
	}
	var k tea.KeyPressMsg
	for _, mod := range parts[:len(parts)-1] {
		switch mod {
		case "ctrl":
			k.Mod |= tea.ModCtrl
		case "alt":
			k.Mod |= tea.ModAlt
		case "shift":
			k.Mod |= tea.ModShift
		}
	}
	if code, ok := keyNames[base]; ok {
		k.Code = code
	} else if r := []rune(base); len(r) == 1 {
		k.Code = r[0]
		if k.Mod == 0 {
			k.Text = base
		}
	}
	if k.String() != name {
		t.Fatalf("key %q is sent as %q", name, k.String())
	}
	return k
}

// submit types a line and presses Enter.
func (a *app) submit(text string) {
	a.typeText(text)
	a.key("enter")
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

// prompted is whether the screen shows the user's prompt as its row,
// "you" over the text, as the record has it.
func prompted(text string) func(string) bool {
	return regexp.MustCompile(`(?m)^you +\n` + regexp.QuoteMeta(text) + ` *$`).MatchString
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
