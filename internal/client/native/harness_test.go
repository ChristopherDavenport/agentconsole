package native_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentsession/jsonl"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/internal/client"
	"github.com/ChristopherDavenport/agentconsole/internal/client/native"
	"github.com/ChristopherDavenport/agentconsole/internal/view"
)

const wait = 10 * time.Second

// gates lets a test hold a scripted model or tool at a named point and
// release it, so the rendered model can be asserted between two steps
// of one run.
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

// hold is called by the script: it reports that name was reached and
// blocks until the test releases it.
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

// arrive blocks the test until the script holds at name.
func (g *gates) arrive(t *testing.T, name string) {
	t.Helper()
	reached, _ := g.chans(name)
	select {
	case <-reached:
	case <-time.After(wait):
		t.Fatalf("script never reached %q", name)
	}
}

// release lets the script go on. It is safe to call twice, so a test can
// release in a cleanup as well, which a failed test needs: a script parked
// at a gate would hold the run, and the rig's cleanup waits for the run.
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

// script is a model that answers each request with the next of its
// responses.
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

// say streams a message in chunks, holding at each gate named in holds
// after the chunk of the same index.
func say(g *gates, holds map[int]string, chunks ...string) func(context.Context, *openresponses.Emitter) error {
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

// callTool streams one function call.
func callTool(callID, name, args string) func(context.Context, *openresponses.Emitter) error {
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

// nameless drops the events that name a response before its items, as a
// stream that sends no response.created does.
type nameless struct{ m openresponses.Streamer }

func (n nameless) CreateStream(ctx context.Context, req openresponses.Request, sink openresponses.EventSink) error {
	return n.m.CreateStream(ctx, req, openresponses.EventSinkFunc(func(ev openresponses.StreamEvent) error {
		switch ev.(type) {
		case *openresponses.ResponseCreatedEvent, *openresponses.ResponseInProgressEvent:
			return nil
		}
		return sink.Send(ev)
	}))
}

// rig is a real agent with a real recorder over a memory store, a native
// backend, and a view fed from both of its streams on goroutines of
// their own, as a client would. Every state the view passes through is
// kept, and the invariants the reconciler promises are checked on each.
type rig struct {
	t      *testing.T
	ctx    context.Context
	store  agentsession.Store
	sess   *agentsession.Session
	rec    *session.Recorder
	agent  *agentturn.Agent
	be     *native.Backend
	ctl    client.Control
	cancel context.CancelFunc

	// allowDrop lifts the rule that a row once shown stays shown, for a
	// test that moves to another branch.
	allowDrop bool

	mu      sync.Mutex
	changed *sync.Cond
	v       *view.View
	history []view.Model
	seen    map[string]bool
	fault   string
	snapped bool
	runs    sync.WaitGroup
}

func newRig(t *testing.T, cfg agentturn.Config) *rig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	// A jsonl store, which the follower reads back from the file. The
	// memory store shares its entries with the follower, and the recorder
	// sets an entry's ID after appending it: a data race the race
	// detector reports and this client cannot fix.
	store, err := jsonl.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rec, sess, err := session.Start(ctx, store, agentsession.Header{Records: agentsession.AllRecords})
	if err != nil {
		t.Fatal(err)
	}
	a := agentturn.New(cfg)
	detach := rec.Attach(a)
	be, err := native.New(a, rec)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, ctx: ctx, store: store, sess: sess, rec: rec, agent: a, be: be, ctl: be.Control(),
		cancel: cancel, v: view.New(), seen: map[string]bool{}}
	r.changed = sync.NewCond(&r.mu)

	live := be.Live(ctx)
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		for ev, err := range live {
			if err != nil {
				return
			}
			r.apply(func() { r.v.Live(ev) })
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		for ch, err := range be.Record().Follow(ctx, "") {
			if err != nil {
				return
			}
			r.apply(func() {
				r.v.Record(ch)
				r.snapped = true
			})
		}
	}()
	t.Cleanup(func() {
		if t.Failed() {
			cancel() // a run parked at a gate must not outlive a failed test
		}
		r.runs.Wait()
		cancel()
		<-done
		<-done
		detach()
		if err := store.Close(); err != nil {
			t.Errorf("close the store: %v", err)
		}
		if r.fault != "" {
			t.Error(r.fault)
		}
	})
	r.waitFor("the snapshot", func(view.Model) bool { return r.snapped })
	return r
}

// apply runs one step of the view and checks the invariants on the state
// it leaves.
func (r *rig) apply(step func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	step()
	m := r.v.Model()
	r.history = append(r.history, m)
	r.check(m)
	r.changed.Broadcast()
}

// check fails the test when a row is shown twice, or when a row that was
// shown is no longer.
func (r *rig) check(m view.Model) {
	now := map[string]bool{}
	for _, row := range m.Rows {
		k := rowKey(row)
		if k == "" {
			continue
		}
		if now[k] && r.fault == "" {
			r.fault = fmt.Sprintf("row %s is shown twice in %s", k, describe(m))
		}
		now[k] = true
	}
	if !r.allowDrop {
		for k := range r.seen {
			if !now[k] && r.fault == "" {
				r.fault = fmt.Sprintf("row %s was shown and then dropped, leaving %s", k, describe(m))
			}
		}
	}
	for k := range now {
		r.seen[k] = true
	}
}

// rowKey is what identifies a row across the record and the overlay.
func rowKey(row view.Row) string {
	if row.Call != nil {
		return "call:" + row.Call.CallID
	}
	if out, ok := row.Item.(*openresponses.FunctionCallOutput); ok {
		return "out:" + out.CallID
	}
	if id := client.ItemID(row.Item); id != "" {
		return "item:" + id
	}
	return ""
}

func describe(m view.Model) string {
	var b strings.Builder
	for _, row := range m.Rows {
		live := ""
		if row.Live {
			live = "~"
		}
		fmt.Fprintf(&b, "[%s%s %s]", live, row.Item.ItemType(), rowKey(row))
	}
	return b.String()
}

// waitFor blocks until the view's model satisfies cond.
func (r *rig) waitFor(what string, cond func(view.Model) bool) view.Model {
	r.t.Helper()
	deadline := time.AfterFunc(wait, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.changed.Broadcast()
	})
	defer deadline.Stop()
	start := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		m := r.v.Model()
		if cond(m) {
			return m
		}
		if time.Since(start) > wait {
			r.t.Fatalf("timed out waiting for %s; the model is %s, turn %+v, %d permissions", what, describe(m), m.Turn, len(m.Permissions))
		}
		r.changed.Wait()
	}
}

// ever reports whether any state the view passed through satisfies cond.
func (r *rig) ever(cond func(view.Model) bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.history {
		if cond(m) {
			return true
		}
	}
	return false
}

// prompt starts a run on its own goroutine and returns what it ends with.
func (r *rig) prompt(text string) <-chan error {
	return r.start(func() error { return r.ctl.Prompt(r.ctx, openresponses.UserText(text)) })
}

func (r *rig) start(f func() error) <-chan error {
	out := make(chan error, 1)
	r.runs.Add(1)
	go func() {
		defer r.runs.Done()
		out <- f()
	}()
	return out
}

func (r *rig) finish(run <-chan error) {
	r.t.Helper()
	select {
	case err := <-run:
		if err != nil {
			r.t.Fatalf("run: %v", err)
		}
	case <-time.After(wait):
		r.t.Fatal("the run did not end")
	}
}

// idle is the state of a view with a finished run and nothing in flight.
func idle(m view.Model) bool {
	if m.Turn.State == view.Running {
		return false
	}
	for _, row := range m.Rows {
		if row.Live {
			return false
		}
	}
	return true
}

func text(item openresponses.Item) string {
	switch v := item.(type) {
	case *openresponses.Message:
		return v.Text()
	case *openresponses.ReasoningItem:
		return v.Summary.Text()
	}
	return ""
}

// assistantRows are the rows of the assistant's messages.
func assistantRows(m view.Model) []view.Row {
	var out []view.Row
	for _, row := range m.Rows {
		if msg, ok := row.Item.(*openresponses.Message); ok && msg.Role == openresponses.RoleAssistant {
			out = append(out, row)
		}
	}
	return out
}
