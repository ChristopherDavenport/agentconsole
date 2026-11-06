package client

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentsession/jsonl"
	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"
	"github.com/ChristopherDavenport/openresponses/echo"
)

// blocking is a model whose call waits for its context to end.
type blocking struct{ called chan struct{} }

func (b blocking) CreateStream(ctx context.Context, _ openresponses.Request, _ openresponses.EventSink) error {
	close(b.called)
	<-ctx.Done()
	return ctx.Err()
}

func idleWithin(t *testing.T, a *agentturn.Agent) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for a.State().Running {
		if time.Now().After(deadline) {
			t.Fatal("the run did not end")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestDirectReturnsOnceTheRunStarted: a Prompt is taken when its run has
// started, not when it ends; a second Prompt while it goes is refused
// with the control's own error; Abort ends the run.
func TestDirectReturnsOnceTheRunStarted(t *testing.T) {
	m := blocking{called: make(chan struct{})}
	a := agentturn.New(agentturn.Config{Model: m, ModelName: "m"})
	act := Direct(a)
	ctx := context.Background()
	if err := act(ctx, Prompt{Items: []openresponses.Item{openresponses.UserText("hi")}}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if !a.State().Running {
		t.Fatal("Direct returned before the run started")
	}
	err := act(ctx, Prompt{Items: []openresponses.Item{openresponses.UserText("again")}})
	if !errors.Is(err, agentturn.ErrRunning) {
		t.Fatalf("a prompt while a run goes: %v, want ErrRunning", err)
	}
	<-m.called
	if err := act(ctx, Abort{}); err != nil {
		t.Fatalf("abort: %v", err)
	}
	idleWithin(t, a)
}

// TestDirectRefusesWhatFailsBeforeAStart: an answer with nothing
// pending never starts a run, and its error is the refusal.
func TestDirectRefusesWhatFailsBeforeAStart(t *testing.T) {
	a := agentturn.New(agentturn.Config{Model: &echo.Adapter{}, ModelName: "echo"})
	if err := Direct(a)(context.Background(), Answer{Calls: []CallAnswer{{CallID: "nope", Verdict: VerdictApprove}}}); err == nil {
		t.Fatal("an answer with nothing pending was taken")
	}
}

// TestDirectRefusesAHeadMoveWithoutTheMethod: a bare agent cannot move
// its head, so ContinueFrom is refused with ErrNoHeadMove.
func TestDirectRefusesAHeadMoveWithoutTheMethod(t *testing.T) {
	a := agentturn.New(agentturn.Config{Model: &echo.Adapter{}, ModelName: "echo"})
	if err := Direct(a)(context.Background(), ContinueFrom{EntryID: "e1"}); !errors.Is(err, ErrNoHeadMove) {
		t.Fatalf("continue from: %v, want ErrNoHeadMove", err)
	}
}

type mover struct {
	agentturn.Control
	to string
}

func (m *mover) ContinueFrom(_ context.Context, entryID string) error {
	m.to = entryID
	return nil
}

// TestDirectMovesTheHeadThroughTheControl: a control with the method
// gets the move.
func TestDirectMovesTheHeadThroughTheControl(t *testing.T) {
	m := &mover{Control: agentturn.New(agentturn.Config{Model: &echo.Adapter{}, ModelName: "echo"})}
	if err := Direct(m)(context.Background(), ContinueFrom{EntryID: "e1"}); err != nil || m.to != "e1" {
		t.Fatalf("continue from: %v, moved to %q", err, m.to)
	}
}

// TestDirectQueuesAndReplies: Queue and Reply are the control's own.
func TestDirectQueuesAndReplies(t *testing.T) {
	a := agentturn.New(agentturn.Config{Model: &echo.Adapter{}, ModelName: "echo"})
	act := Direct(a)
	if err := act(context.Background(), Queue{Mode: agentturn.QueueFollowUp, Items: []openresponses.Item{openresponses.UserText("later")}}); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := act(context.Background(), Reply{ID: "q-none"}); !errors.Is(err, agentturn.ErrNoQuestion) {
		t.Fatalf("reply to no question: %v, want ErrNoQuestion", err)
	}
}

// RecordOf over a store lists the sessions made from the followed one,
// through the optional Lister.
func TestRecordOfListsTheSessionsMadeFromOne(t *testing.T) {
	ctx := context.Background()
	store := agentsession.NewMemoryStore()
	rec, s, err := session.Start(ctx, store, agentsession.Header{})
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.Append(ctx, s.ID(), agentsession.NewItemEntry(openresponses.UserText("hi")))
	if err != nil {
		t.Fatal(err)
	}
	fork, err := store.Create(ctx, agentsession.Header{ParentSession: s.ID(), Base: id})
	if err != nil {
		t.Fatal(err)
	}
	r, err := RecordOf(rec)
	if err != nil {
		t.Fatal(err)
	}
	l, ok := r.(Lister)
	if !ok {
		t.Fatal("RecordOf over a store is no Lister")
	}
	got, err := l.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Header.ID != fork.ID() || got[0].Header.Base != id {
		t.Errorf("listed %+v, want the fork", got)
	}
}

// accepting counts the steers a control took through Accept and the
// inputs it took through Queue.
type accepting struct {
	*agentturn.Agent
	accepts, queues int
}

func (c *accepting) Accept(ctx context.Context, items ...openresponses.Item) (*agentturn.Delivery, error) {
	c.accepts++
	return c.Agent.Accept(ctx, items...)
}

func (c *accepting) Queue(ctx context.Context, mode agentturn.QueueMode, items ...openresponses.Item) error {
	c.queues++
	return c.Agent.Queue(ctx, mode, items...)
}

// TestDirectSteersThroughAnAccepter: a steer goes through the control's
// Accept when it offers one (agentturn#302), a follow-up through Queue,
// which Accept does not take (#50).
func TestDirectSteersThroughAnAccepter(t *testing.T) {
	c := &accepting{Agent: agentturn.New(agentturn.Config{Model: &echo.Adapter{}, ModelName: "echo"})}
	act := Direct(c)
	if err := act(context.Background(), Queue{Mode: agentturn.QueueFollowUp, Items: []openresponses.Item{openresponses.UserText("later")}}); err != nil {
		t.Fatalf("follow-up: %v", err)
	}
	if err := act(context.Background(), Queue{Mode: agentturn.QueueSteer, Items: []openresponses.Item{openresponses.UserText("now")}}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	if c.accepts != 1 || c.queues != 1 {
		t.Errorf("accepts %d, queues %d; want the steer accepted and the follow-up queued", c.accepts, c.queues)
	}
	idleWithin(t, c.Agent)
}

// TestTwoSteersDuringAQuietToolAreBothTaken: two steers sent while a
// tool runs and emits nothing are each answered before the tool ends,
// and each is on the record as a delivery, queued, before it ends (#50).
func TestTwoSteersDuringAQuietToolAreBothTaken(t *testing.T) {
	ctx := context.Background()
	// A jsonl store, read back from the file: the memory store shares
	// its entries with a reader while the recorder writes.
	store, err := jsonl.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rec, s, err := session.Start(ctx, store, agentsession.Header{})
	if err != nil {
		t.Fatal(err)
	}
	reached, release := make(chan struct{}), make(chan struct{})
	quiet := agenttool.New("quiet", "waits", func(ctx context.Context, _ struct{}) (string, error) {
		close(reached)
		select {
		case <-release:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	model := &twoStep{}
	a := agentturn.New(agentturn.Config{Model: model, ModelName: "m", Tools: []agenttool.Tool{quiet}})
	defer rec.Attach(a)()
	act := Direct(a)
	if err := act(ctx, Prompt{Items: []openresponses.Item{openresponses.UserText("go")}}); err != nil {
		t.Fatal(err)
	}
	<-reached
	for _, text := range []string{"also this", "and that"} {
		returned := make(chan error, 1)
		go func() {
			returned <- act(ctx, Queue{Mode: agentturn.QueueSteer, Items: []openresponses.Item{openresponses.UserText(text)}})
		}()
		select {
		case err := <-returned:
			if err != nil {
				t.Fatalf("steer %q: %v", text, err)
			}
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatalf("steer %q waited on the tool", text)
		}
	}
	// A bare agent's recorder writes the entry at the queued report,
	// which comes at once, on a goroutine of its own, while the tool
	// runs.
	var deliveries int
	for deadline := time.Now().Add(5 * time.Second); deliveries < 2 && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		read, err := store.Read(ctx, s.ID())
		if err != nil {
			t.Fatal(err)
		}
		deliveries = 0
		for _, q := range agentsession.Queued(read.Path(read.Leaf())) {
			if q.Deliver {
				deliveries++
			}
		}
	}
	close(release)
	if deliveries != 2 {
		t.Errorf("%d steers on the record as deliveries before the tool ended, want 2", deliveries)
	}
	idleWithin(t, a)
}

// twoStep calls the quiet tool, then answers.
type twoStep struct{ called bool }

func (m *twoStep) CreateStream(_ context.Context, req openresponses.Request, sink openresponses.EventSink) error {
	em := openresponses.NewEmitter(sink, openresponses.NewResponse(req))
	if !m.called {
		m.called = true
		w, err := em.FunctionCall("call_1", "quiet")
		if err != nil {
			return err
		}
		if err := w.Arguments(`{}`); err != nil {
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
		return em.Complete()
	}
	w, err := em.Message(openresponses.PhaseFinalAnswer)
	if err != nil {
		return err
	}
	if err := w.Text("ok"); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return em.Complete()
}

// TestDirectRefusesTheNextMode: an agentturn Control has no modes, so
// the next-mode intent is the embedder's to handle; Direct refuses it.
func TestDirectRefusesTheNextMode(t *testing.T) {
	a := agentturn.New(agentturn.Config{Model: blocking{called: make(chan struct{})}, ModelName: "m"})
	if err := Direct(a)(context.Background(), NextMode{}); !errors.Is(err, ErrNoModes) {
		t.Fatalf("next mode: %v, want ErrNoModes", err)
	}
}
