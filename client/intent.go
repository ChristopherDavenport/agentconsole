package client

import (
	"context"
	"errors"
	"fmt"

	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"
)

// Intent is what the user asked of the agent, as a plain value: the view
// sends it to its [Controller], which decides what it does.
type Intent interface{ intent() }

// Prompt asks for a run with Items.
type Prompt struct{ Items []openresponses.Item }

// Queue queues Items into the run that goes, or the next one, as Mode
// says: a steer, taken before the run's next model call, or a
// follow-up, taken when the run would stop.
type Queue struct {
	Mode  agentturn.QueueMode
	Items []openresponses.Item
}

// Abort asks for the run that goes to stop.
type Abort struct{}

// Answer answers the calls a run left waiting and asks for the run to go
// on: the permission panel's answers, one per call it asked about.
type Answer struct{ Answers []agentturn.Answer }

// Reply answers the question ID a running call asked
// ([agentturn.Question]); the user's words are the answer's Note.
type Reply struct {
	ID     string
	Answer agenttool.Answer
}

// ContinueFrom asks to move the agent's head to EntryID: the next prompt
// continues the conversation from there, on a branch of the session.
type ContinueFrom struct{ EntryID string }

func (Prompt) intent()       {}
func (Queue) intent()        {}
func (Abort) intent()        {}
func (Answer) intent()       {}
func (Reply) intent()        {}
func (ContinueFrom) intent() {}

// Controller acts on an intent. It returns promptly: nil says the intent
// was taken, and an error is a refusal, which the client shows. What
// happens after (a run starting, going, ending, a question closing, the
// head moving) reaches the client as events and record entries, never as
// a return value. A product writes its own: one that answers some
// questions by rule, records an answer as a person's, or forwards to an
// agent elsewhere.
type Controller func(ctx context.Context, in Intent) error

// ErrNoHeadMove refuses a [ContinueFrom] to a control that cannot move
// the agent's head.
var ErrNoHeadMove = errors.New("client: moving the head is not offered by this agent's control")

// headMover is a control that moves the agent's head, which
// [agentturn.Control] does not: agentkit's Control does.
type headMover interface {
	ContinueFrom(ctx context.Context, entryID string) error
}

// Direct is the Controller that maps each intent onto c:
//
//   - Prompt and Answer start c.Prompt and c.Resume on a goroutine of
//     their own, under ctx, and return once the run has started (its
//     run_start, seen through a subscription to c) or the call has
//     failed before it started, with that error ([agentturn.ErrRunning],
//     say). An error after the start reaches the client in the run's
//     [agentturn.RunEnd].
//   - Queue, Abort and Reply are c's own, which return at once. Reply
//     goes under ctx, which names who answered when it carries
//     [agentturn.ContextWithAnswerer]: the view holds no identity, so
//     an embedder that knows the person wraps the Controller and adds
//     it to a Reply's context. Without it the answer is unattributed.
//   - ContinueFrom goes to c when c has a ContinueFrom(ctx, entryID)
//     method (agentkit's Control has), and is refused with
//     [ErrNoHeadMove] otherwise.
//
// The run is under ctx: ending ctx ends the run, as it does an
// in-process Prompt's.
func Direct(c agentturn.Control) Controller {
	return func(ctx context.Context, in Intent) error {
		switch in := in.(type) {
		case Prompt:
			return start(ctx, c, func(ctx context.Context) error {
				_, err := c.Prompt(ctx, in.Items...)
				return err
			})
		case Answer:
			return start(ctx, c, func(ctx context.Context) error {
				_, err := c.Resume(ctx, in.Answers...)
				return err
			})
		case Queue:
			return c.Queue(ctx, in.Mode, in.Items...)
		case Abort:
			c.Abort()
			return nil
		case Reply:
			return c.Reply(ctx, in.ID, in.Answer)
		case ContinueFrom:
			if h, ok := c.(headMover); ok {
				return h.ContinueFrom(ctx, in.EntryID)
			}
			return ErrNoHeadMove
		case nil:
			return errors.New("client: no intent")
		}
		return fmt.Errorf("client: unknown intent %T", in)
	}
}

// start runs call on a goroutine and returns once c reports a run
// started, or call returned first: its error, nil when a run started and
// ended before the start was seen.
func start(ctx context.Context, c agentturn.Control, call func(context.Context) error) error {
	started := make(chan struct{})
	unsub := c.Subscribe(func(_ context.Context, ev agentturn.Event) error {
		if _, ok := ev.(*agentturn.RunStart); ok {
			select {
			case <-started:
			default:
				close(started)
			}
		}
		return nil
	})
	defer unsub()
	done := make(chan error, 1)
	go func() { done <- call(ctx) }()
	select {
	case err := <-done:
		return err
	case <-started:
		return nil
	}
}
