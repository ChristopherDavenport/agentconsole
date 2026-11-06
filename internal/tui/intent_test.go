package tui_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentsession/jsonl"
	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client"
	"github.com/ChristopherDavenport/agentconsole/internal/tui"
	"github.com/ChristopherDavenport/agentconsole/view"
)

// handSource is an event source the test feeds by hand: no agent.
type handSource struct {
	mu    sync.Mutex
	subs  map[int]func(context.Context, agentturn.Event) error
	n     int
	state agentturn.State
}

func (s *handSource) Subscribe(fn func(context.Context, agentturn.Event) error) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subs == nil {
		s.subs = map[int]func(context.Context, agentturn.Event) error{}
	}
	s.n++
	id := s.n
	s.subs[id] = fn
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.subs, id)
	}
}

func (s *handSource) State() agentturn.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// emit delivers evs to every subscriber, keeping State's Running in step
// with the runs they start and end.
func (s *handSource) emit(evs ...agentturn.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range evs {
		switch ev.(type) {
		case *agentturn.RunStart:
			s.state.Running = true
		case *agentturn.RunEnd:
			s.state.Running = false
		}
		for _, fn := range s.subs {
			_ = fn(context.Background(), ev)
		}
	}
}

// intents is a controller that keeps what it is sent, and refuses it
// while refuse is set.
type intents struct {
	mu     sync.Mutex
	got    []client.Intent
	refuse error
}

func (c *intents) act(_ context.Context, in client.Intent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, in)
	return c.refuse
}

func (c *intents) setRefuse(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refuse = err
}

// next waits for the n-th intent (from 1) and returns it.
func (c *intents) next(t *testing.T, n int) client.Intent {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		c.mu.Lock()
		if len(c.got) >= n {
			in := c.got[n-1]
			c.mu.Unlock()
			return in
		}
		c.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("intent %d never sent", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// newViewApp is the TUI over a record of an empty session, src and act:
// a view with no agent behind it.
func newViewApp(t *testing.T, src client.Source, act client.Controller, opts ...tui.Option) *app {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	store, err := jsonl.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rec, _, err := session.Start(ctx, store, agentsession.Header{Records: agentsession.AllRecords})
	if err != nil {
		t.Fatal(err)
	}
	record, err := client.RecordOf(rec)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{t: t, ctx: ctx, cancel: cancel, store: store, rec: rec, record: record}
	a.m = tui.New(ctx, record, src, act, append([]tui.Option{tui.WithPanelSettle(0)}, opts...)...)
	a.wait = tui.Attach(ctx, record, src, a.send)
	t.Cleanup(func() {
		cancel()
		a.wait()
		a.wg.Wait()
		if err := store.Close(); err != nil {
			t.Errorf("close the store: %v", err)
		}
	})
	a.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	return a
}

// askAbout plays a run that ends asking about one call.
func askAbout(src *handSource, run, callID string) {
	call := &openresponses.FunctionCall{CallID: callID, Name: "rm", Arguments: `{"path":"build"}`}
	d := &agentturn.ToolDecision{Action: agentturn.Defer, Reason: "approval required by rm"}
	src.emit(
		&agentturn.RunStart{RunID: run},
		&agentturn.ToolStart{RunID: run, CallID: callID, Name: "rm", Args: json.RawMessage(call.Arguments), Decision: d},
		&agentturn.ToolEnd{RunID: run, CallID: callID, Name: "rm", Deferred: true, Reason: d.Reason},
		&agentturn.RunEnd{RunID: run, Reason: agentturn.ReasonInputRequired,
			Pending: []agentturn.PendingCall{{Call: call, Reason: agentturn.PendingDeferred, Decision: d}}},
	)
}

// TestTheViewSendsIntentsAndHoldsNoControl: the client with no agent at
// all, fed events by hand. What the user does leaves as intents to the
// controller, and a refusal is shown; what follows is what the events
// say.
func TestTheViewSendsIntentsAndHoldsNoControl(t *testing.T) {
	src := &handSource{}
	ctl := &intents{}
	a := newViewApp(t, src, ctl.act)
	n := 0
	next := func() client.Intent { n++; return ctl.next(t, n) }

	// A prompt the controller refuses: the refusal is shown, and nothing
	// is going.
	ctl.setRefuse(errors.New("busy elsewhere"))
	a.submit("hi")
	if in, ok := next().(client.Prompt); !ok || len(in.Items) != 1 {
		t.Fatalf("intent = %#v, want the prompt", in)
	}
	a.waitFor("the refusal", has("run: busy elsewhere", "idle"))
	ctl.setRefuse(nil)

	// A permission approved: y gives Answer with the call approved.
	askAbout(src, "run1", "c1")
	a.waitFor("the permission", has("Permission requested (1/1): rm", "approval required by rm"))
	a.typeText("y")
	in, ok := next().(client.Answer)
	if !ok || len(in.Calls) != 1 || in.Calls[0] != (client.CallAnswer{CallID: "c1", Verdict: client.VerdictApprove}) {
		t.Fatalf("intent = %#v, want c1 approved once", in)
	}

	// Refused with a reason: a refusal carrying it, the run to go on.
	askAbout(src, "run2", "c2")
	a.waitFor("the second permission", has("Permission requested (1/1): rm"))
	a.typeText("n")
	a.typeText("too risky")
	a.key("enter")
	in, ok = next().(client.Answer)
	if !ok || len(in.Calls) != 1 || in.Calls[0] != (client.CallAnswer{CallID: "c2", Verdict: client.VerdictRefuse, Reason: "too risky"}) {
		t.Fatalf("intent = %#v, want c2 refused with the reason", in)
	}

	// A question allowed with a note: Reply with accept and the note.
	src.emit(&agentturn.RunStart{RunID: "run3"}, &agentturn.Question{ID: "q1", RunID: "run3",
		Elicitation: agenttool.Elicitation{Message: "may it write notes.md?"}})
	a.waitFor("the question", has("Question (1/1)", "may it write notes.md?"))
	a.typeText("a")
	a.typeText("only the notes")
	a.key("enter")
	if in, ok := next().(client.Reply); !ok || in.ID != "q1" || in.Answer.Action != agenttool.ActionAccept || in.Answer.Note != "only the notes" || in.Answer.By != agenttool.ByHuman {
		t.Fatalf("intent = %#v, want q1 accepted with the note", in)
	}
	src.emit(&agentturn.QuestionClosed{ID: "q1", RunID: "run3"})

	// A line typed while the run goes: a steer.
	a.waitFor("the run going", all(has("waiting"), lacks("Question (1/1)")))
	a.submit("also this")
	if in, ok := next().(client.Queue); !ok || in.Mode != agentturn.QueueSteer || len(in.Items) != 1 {
		t.Fatalf("intent = %#v, want a steer", in)
	}

	// The abort key: Abort, and the view goes idle when the run's end
	// says so, not before.
	a.key("ctrl+c")
	if _, ok := next().(client.Abort); !ok {
		t.Fatalf("intent %d is not an abort", n)
	}
	a.waitFor("aborting", has("aborting"))
	src.emit(&agentturn.RunEnd{RunID: "run3", Reason: agentturn.ReasonAborted, Err: context.Canceled})
	a.waitFor("idle again", all(has("idle"), lacks("aborting")))
}

// TestThePanelOffersTheHostsGrant: with a host that offers a rule, the
// panel shows it beside g and G, and g sends the approval with a session
// grant of that rule; with none it offers only once (#41). n refuses and
// lets the run go on, s refuses and stops it, each with a reason (#42).
func TestThePanelOffersTheHostsGrant(t *testing.T) {
	src := &handSource{}
	ctl := &intents{}
	offer := func(_ context.Context, p view.Permission) client.GrantOffer {
		if p.CallID == "c1" {
			return client.GrantOffer{Rule: "bash(go test:*)", Saved: true}
		}
		return client.GrantOffer{}
	}
	a := newViewApp(t, src, ctl.act, tui.WithGrantOffer(offer))
	n := 0
	next := func() client.Intent { n++; return ctl.next(t, n) }

	askAbout(src, "run1", "c1")
	a.waitFor("the offer", has("Permission requested (1/1): rm", "[g] always bash(go test:*) this session", "[G] and save it", "[n] refuse", "[s] refuse and stop"))
	a.typeText("g")
	in, ok := next().(client.Answer)
	want := client.CallAnswer{CallID: "c1", Verdict: client.VerdictApprove, Grant: client.GrantSession, Rule: "bash(go test:*)"}
	if !ok || len(in.Calls) != 1 || in.Calls[0] != want {
		t.Fatalf("intent = %#v, want c1 approved with a session grant", in)
	}

	// No rule offered: no g, and g does nothing.
	askAbout(src, "run2", "c2")
	s := a.waitFor("the plain panel", all(has("Permission requested (1/1): rm", "[y] approve"), lacks("[g]", "[G]")))
	a.typeText("g")
	a.typeText("G")
	if s2 := a.screen(); !strings.Contains(s2, "[y] approve") {
		t.Fatalf("g answered with no rule offered:\n%s\nwas\n%s", s2, s)
	}

	// s refuses and stops, with a reason.
	a.typeText("s")
	a.waitFor("the stop's reason", has("Reason for refusing and stopping the run"))
	a.typeText("I will do it")
	a.key("enter")
	in, ok = next().(client.Answer)
	if !ok || len(in.Calls) != 1 || in.Calls[0] != (client.CallAnswer{CallID: "c2", Verdict: client.VerdictStop, Reason: "I will do it"}) {
		t.Fatalf("intent = %#v, want c2 refused and stopped", in)
	}

	// G saves the grant.
	askAbout(src, "run3", "c1")
	a.waitFor("the offer again", has("[G] and save it"))
	a.typeText("G")
	in, ok = next().(client.Answer)
	want.Grant = client.GrantSaved
	if !ok || len(in.Calls) != 1 || in.Calls[0] != want {
		t.Fatalf("intent = %#v, want c1 approved with a saved grant", in)
	}
}
