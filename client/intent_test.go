package client

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ChristopherDavenport/agentturn"
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
	if err := Direct(a)(context.Background(), Answer{Answers: []agentturn.Answer{agentturn.Approve("nope")}}); err == nil {
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
