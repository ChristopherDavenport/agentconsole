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
// on: the permission panel's answers, one per call it asked about. Each
// says what the person meant, not the control value: the controller
// builds that ([Direct] with [AnswerOf], or a product's own words), and
// makes any grant an approval asks for.
type Answer struct{ Calls []CallAnswer }

// CallAnswer is the person's answer to one call, as the permission panel
// took it.
type CallAnswer struct {
	CallID  string
	Verdict Verdict
	// Reason is what the person typed when refusing, which may be empty.
	Reason string
	// Grant, for an approval, is how far it reaches beyond this call,
	// and Rule the rule the host offered for it ([GrantOffer]), in the
	// host's own spelling. GrantOnce, the zero value, has no rule.
	Grant Grant
	Rule  string
}

// Verdict is what the person decided about a call.
type Verdict string

const (
	// VerdictApprove runs the call.
	VerdictApprove Verdict = "approve"
	// VerdictRefuse refuses the call and lets the run go on: the model
	// reads the refusal and the reason, and may try another way.
	VerdictRefuse Verdict = "refuse"
	// VerdictStop refuses the call and ends the run, for a person who
	// wants to take over (agentturn's StopRefused).
	VerdictStop Verdict = "stop"
)

// Grant is how far an approval reaches.
type Grant string

const (
	// GrantOnce approves this call only.
	GrantOnce Grant = ""
	// GrantSession approves this call and, for the rest of the session,
	// the calls the offered rule matches.
	GrantSession Grant = "session"
	// GrantSaved is GrantSession kept beyond the session, where the host
	// keeps it (dax: for the project).
	GrantSaved Grant = "saved"
)

// GrantOffer is the grant a host offers for one permission: Rule, the
// rule an approval would grant, in the host's spelling (agentpolicy's
// "bash(go test:*)", say), and Saved, whether the host can keep it
// beyond the session. An empty Rule offers none, and the panel offers
// only "once".
type GrantOffer struct {
	Rule  string
	Saved bool
}

// Reply answers the question ID a running call asked
// ([agentturn.Question]); the user's words are the answer's Note.
type Reply struct {
	ID     string
	Answer agenttool.Answer
}

// ContinueFrom asks to move the agent's head to EntryID: the next prompt
// continues the conversation from there, on a branch of the session.
type ContinueFrom struct{ EntryID string }

// NextMode asks the host to move to its next mode: a set of rules for
// what the agent may do that the host names and cycles (dax's default,
// plan and bypass, say). It names no mode, since the modes are the
// host's and the view knows none; the host shows the one it moved to
// in a status line segment of its own. A switch during a run is the
// host's to time (agentpolicy's SetPolicy, with a reason, at the next
// turn boundary).
type NextMode struct{}

func (Prompt) intent()       {}
func (Queue) intent()        {}
func (Abort) intent()        {}
func (Answer) intent()       {}
func (Reply) intent()        {}
func (ContinueFrom) intent() {}
func (NextMode) intent()     {}

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

// ErrNoModes refuses a [NextMode] in [Direct]: an [agentturn.Control]
// has no modes, which are the host's.
var ErrNoModes = errors.New("client: this agent's control has no modes to switch")

// headMover is a control that moves the agent's head, which
// [agentturn.Control] does not: agentkit's Control does.
type headMover interface {
	ContinueFrom(ctx context.Context, entryID string) error
}

// DirectOption configures [Direct].
type DirectOption func(*direct)

type direct struct {
	answer func(CallAnswer) agentturn.Answer
}

// WithAnswers builds the control's answer to each call of an [Answer]
// with fn in place of [AnswerOf], so the model reads a product's own
// words for a refusal, the same in every front it has. fn may hand an
// answer it has no words for to AnswerOf.
func WithAnswers(fn func(CallAnswer) agentturn.Answer) DirectOption {
	return func(d *direct) { d.answer = fn }
}

// who is what the record says answered at the permission panel: a
// person, by the keys they pressed.
const who = "human"

// AnswerOf is [Direct]'s answer to one call, marked By "human", since
// the panel's answers are the person's:
//
//   - VerdictApprove approves the call; the grant, if any, is not the
//     control's and is left out (the product's controller makes it).
//   - VerdictRefuse answers with an output, "The user refused this
//     call.", and the reason after it, and the run goes on.
//   - VerdictStop answers with the same output and ends the run
//     ([agentturn.Refuse]).
//
// A refusal's reason is also the answer's Reason.
func AnswerOf(a CallAnswer) agentturn.Answer {
	if a.Verdict == VerdictApprove {
		return agentturn.Approve(a.CallID).WithBy(who)
	}
	text := "The user refused this call."
	if a.Reason != "" {
		text += " Reason: " + a.Reason
	}
	out := openresponses.NewFunctionCallOutput(a.CallID, text)
	ans := agentturn.Output(out)
	if a.Verdict == VerdictStop {
		ans = agentturn.Refuse(out)
	}
	ans = ans.WithBy(who)
	if a.Reason != "" {
		ans = ans.WithReason(a.Reason)
	}
	return ans
}

// Direct is the Controller that maps each intent onto c:
//
//   - Prompt and Answer start c.Prompt and c.Resume on a goroutine of
//     their own, under ctx, and return once the run has started (its
//     run_start, seen through a subscription to c) or the call has
//     failed before it started, with that error ([agentturn.ErrRunning],
//     say). An error after the start reaches the client in the run's
//     [agentturn.RunEnd]. An Answer's calls are answered with
//     [AnswerOf], or [WithAnswers]' function, and one with a verdict
//     of none of the three is refused; a grant they ask for is not made, since a control has none to make: a product's
//     controller makes it before it hands the Answer on.
//   - A steer (Queue with [agentturn.QueueSteer]) goes through c's
//     Accept when c is an [agentturn.Accepter] (agentkit's Control,
//     front/control's Client and *agentturn.Agent are): a delivery that
//     returns once the items are queued, written to the record first by
//     a recording control, and that the agent settles on its own, in the
//     run that goes or in one it starts once that run has ended, so a
//     steer that misses the run's last drain is still answered. Its
//     outcome reaches the client as events and entries; nothing waits
//     on it. A follow-up, and a steer to a control without Accept, go
//     through c.Queue.
//   - Queue, Abort and Reply are c's own, which return at once. Reply
//     goes under ctx, which names who answered when it carries
//     [agentturn.ContextWithAnswerer]: the view holds no identity, so
//     an embedder that knows the person wraps the Controller and adds
//     it to a Reply's context. Without it the answer is unattributed.
//   - ContinueFrom goes to c when c has a ContinueFrom(ctx, entryID)
//     method (agentkit's Control has), and is refused with
//     [ErrNoHeadMove] otherwise.
//   - NextMode is refused with [ErrNoModes]: the modes are the host's,
//     whose controller handles it before Direct.
//
// The run is under ctx: ending ctx ends the run, as it does an
// in-process Prompt's. A run Accept starts takes ctx's values without
// its cancellation.
//
// Direct claims nothing for the person: what brought an input in is on
// ctx ([agentturn.ContextWithTrigger]), and the view, holding no
// identity, sets none. An embedder that knows a person typed the line
// wraps the Controller and puts a trigger of kind
// agentsession.TriggerHuman on a Prompt's and a Queue's context, as
// this module's binary does; without it the record holds the input
// unclaimed, which no reader shows as the person's words.
func Direct(c agentturn.Control, opts ...DirectOption) Controller {
	d := direct{answer: AnswerOf}
	for _, o := range opts {
		o(&d)
	}
	return func(ctx context.Context, in Intent) error {
		switch in := in.(type) {
		case Prompt:
			return start(ctx, c, func(ctx context.Context) error {
				_, err := c.Prompt(ctx, in.Items...)
				return err
			})
		case Answer:
			answers := make([]agentturn.Answer, len(in.Calls))
			for i, a := range in.Calls {
				switch a.Verdict {
				case VerdictApprove, VerdictRefuse, VerdictStop:
				default:
					return fmt.Errorf("client: call %s: unknown verdict %q", a.CallID, a.Verdict)
				}
				answers[i] = d.answer(a)
			}
			return start(ctx, c, func(ctx context.Context) error {
				_, err := c.Resume(ctx, answers...)
				return err
			})
		case Queue:
			if a, ok := c.(agentturn.Accepter); ok && in.Mode == agentturn.QueueSteer {
				_, err := a.Accept(ctx, in.Items...)
				return err
			}
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
		case NextMode:
			return ErrNoModes
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
