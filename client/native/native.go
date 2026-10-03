// Package native is the in-process backend: an [agentturn.Agent] driven
// directly, with the [session.Recorder] that writes its store, which is
// followed on the same store value. It is the reference the other
// backends follow.
//
// Control is the agent's. Live is the agent's Subscribe, narrowed by
// [client.FromEvent]. Record is the store's Follow, which wakes on each
// append with no polling.
package native

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client"
)

// Backend drives one agent and follows the session its recorder writes.
type Backend struct {
	agent *agentturn.Agent
	rec   *session.Recorder
	store agentsession.Follower

	mu     sync.Mutex
	models agentturn.ReasoningModels // of the branch the head was moved to

	// afterRebase is a test seam: it runs after the recorder moved.
	afterRebase func() error
}

var _ client.Backend = (*Backend)(nil)

// New returns the backend for agent, whose events rec records. The caller
// attaches rec to agent, with rec.Attach, before the first run: the
// recorder has to be subscribed ahead of this backend, so that an event's
// entry is in the store before the client sees the event. The recorder's
// store must be able to follow: every store of agentsession is.
func New(agent *agentturn.Agent, rec *session.Recorder) (*Backend, error) {
	f, ok := rec.Store().(agentsession.Follower)
	if !ok {
		return nil, fmt.Errorf("native: store %T cannot follow a session", rec.Store())
	}
	return &Backend{agent: agent, rec: rec, store: f}, nil
}

// Agent returns the agent, for what the contract does not carry yet: the
// configuration and the transcript.
func (b *Backend) Agent() *agentturn.Agent { return b.agent }

// SessionID is the ID of the session being recorded.
func (b *Backend) SessionID() string { return b.rec.SessionID() }

// Control implements [client.Backend].
func (b *Backend) Control() client.Control { return control{b} }

// Record implements [client.Backend].
func (b *Backend) Record() client.Record { return record{b} }

type control struct{ b *Backend }

// run carries the reasoning attribution of the branch the agent was last
// moved to, which Agent.SetTranscript cannot take: the loop leaves another
// model's reasoning out of a request by it.
func (c control) run(ctx context.Context) context.Context {
	c.b.mu.Lock()
	defer c.b.mu.Unlock()
	if c.b.models != nil {
		ctx = agentturn.ContextWithReasoningModels(ctx, c.b.models)
	}
	return ctx
}

func (c control) Prompt(ctx context.Context, items ...openresponses.Item) error {
	_, err := c.b.agent.Prompt(c.run(ctx), items...)
	return err
}

func (c control) Steer(items ...openresponses.Item) { c.b.agent.Steer(items...) }

func (c control) Abort() { c.b.agent.Abort() }

func (c control) Answer(ctx context.Context, answers ...agentturn.Answer) error {
	_, err := c.b.agent.Resume(c.run(ctx), answers...)
	return err
}

func (c control) State() agentturn.State { return c.b.agent.State() }

// ContinueFrom moves the head: the recorder is rebased onto the entry
// (which reseeds it from the context there), the agent is given the
// transcript and the pending calls of that branch, and a leaf label is
// appended so the move is on the record. Agentsession's Store has no
// head move of its own (the cas store keeps a head, and nothing on the
// Store interface sets it), so the label is how a follower, and a session
// reopened later, learn where the head is.
//
// The target is checked before anything changes, and a failure after the
// recorder was rebased puts back the leaf, the transcript and the pending
// calls it had, so the session stays usable:
//   - a leaf label, and an entry on a fork's prefix above its base, are
//     refused: agentsession would not let the leaf rest there (its
//     mayRestOn is not exported, so the rule is repeated here);
//   - an entry that leaves function calls without an output is refused: the
//     agent would be left with pending calls nothing in the client can
//     answer, and the next prompt would fail.
//
// The run check and the move are not atomic: agentturn exposes no lock to
// hold across Rebase and SetTranscript, so a run that starts in between
// (nothing in this client does: the TUI is busy for the whole move) finds
// the recorder moved under it. Rebase itself refuses once the recorder has
// seen a run start (ErrRunActive), and SetTranscript refuses while one
// goes; both failures take the undo path.
func (c control) ContinueFrom(ctx context.Context, entryID string) (err error) {
	b := c.b
	if entryID == "" {
		return errors.New("native: continue from: no entry")
	}
	fail := func(err error) error { return fmt.Errorf("native: continue from %s: %w", entryID, err) }
	if b.agent.State().Running {
		return agentturn.ErrRunning
	}
	s, err := b.rec.Store().Open(ctx, b.rec.SessionID())
	if err != nil {
		return fail(err)
	}
	if err := mayRestOn(s, entryID); err != nil {
		return fail(err)
	}
	if calls, err := s.PendingCalls(entryID); err != nil {
		return fail(err)
	} else if len(calls) > 0 {
		return fail(fmt.Errorf("it leaves %d tool call(s) without an output (%s); continue from the answer after it", len(calls), calls[0].Call.Name))
	}

	prevLeaf, prev := s.Leaf(), b.agent.State()
	b.mu.Lock()
	prevModels := b.models
	b.mu.Unlock()
	moved := false
	defer func() {
		if err == nil || !moved {
			return
		}
		// Put back what the move changed. A Rebase into a run appends the
		// run's interrupted end, which stays on the record; the label that
		// follows says the head is back.
		var errs []error
		if rerr := b.rec.Rebase(s, prevLeaf); rerr != nil {
			errs = append(errs, rerr)
		} else if mark, rerr := s.MarkLeaf(); rerr != nil {
			errs = append(errs, rerr)
		} else if _, rerr := b.rec.Store().Append(ctx, b.rec.SessionID(), mark); rerr != nil {
			// Rebase may have closed a run it moved into, which a follower
			// takes as the head moving there; the label says it is back.
			errs = append(errs, rerr)
		}
		if rerr := b.agent.SetTranscript(prev.Transcript); rerr != nil {
			errs = append(errs, rerr)
		}
		if rerr := b.agent.SetPending(prev.Pending); rerr != nil {
			errs = append(errs, rerr)
		}
		b.mu.Lock()
		b.models = prevModels
		b.mu.Unlock()
		if len(errs) > 0 {
			err = fmt.Errorf("%w (and putting the head back failed: %w)", err, errors.Join(errs...))
		}
	}()

	if err := b.rec.Rebase(s, entryID); err != nil {
		return fail(err)
	}
	moved = true
	if b.afterRebase != nil {
		if err := b.afterRebase(); err != nil {
			return fail(err)
		}
	}
	items, models, err := session.TranscriptModels(s)
	if err != nil {
		return fail(err)
	}
	pending, err := session.Pending(s, append(b.rec.ReadOptions(), session.WithContext(ctx))...)
	if err != nil {
		return fail(err)
	}
	if err := b.agent.SetTranscript(items); err != nil {
		return fail(err)
	}
	if err := b.agent.SetPending(pending); err != nil {
		return fail(err)
	}
	b.mu.Lock()
	b.models = models
	b.mu.Unlock()
	mark, err := s.MarkLeaf()
	if err != nil {
		return fail(err)
	}
	if _, err := b.rec.Store().Append(ctx, b.rec.SessionID(), mark); err != nil {
		return fail(fmt.Errorf("mark the head: %w", err))
	}
	return nil
}

// mayRestOn repeats agentsession's rule for where the leaf may rest: an
// entry the session holds that is not a leaf label, and, in a fork, not an
// entry of the prefix above the base.
func mayRestOn(s *agentsession.Session, id string) error {
	e, ok := s.Entry(id)
	if !ok {
		return fmt.Errorf("%w: %s", agentsession.ErrNoEntry, id)
	}
	if l, ok := e.(*agentsession.LabelEntry); ok && l.Label != nil && *l.Label == agentsession.LeafLabel {
		return errors.New("a leaf label is a bookmark, not a place to continue from")
	}
	if base := s.Header().Base; base != "" && id != base && s.Prefix(id) {
		return errors.New("it is on the fork's prefix above the base; the fork can only continue from its base or its own entries")
	}
	return nil
}

type record struct{ b *Backend }

func (r record) Follow(ctx context.Context, from agentsession.Cursor) iter.Seq2[agentsession.Change, error] {
	return r.b.store.Follow(ctx, r.b.rec.SessionID(), from)
}

// Verified is true: the record is the agent's own, written by its
// recorder with request hashes a reader can check.
func (record) Verified() bool { return true }

// Read reads the session through the store's Reader, which takes no hold
// and writes nothing, so it works beside the recorder writing it.
func (r record) Read(ctx context.Context, id string) (*agentsession.Session, error) {
	if id == "" {
		id = r.b.rec.SessionID()
	}
	rd, ok := r.b.rec.Store().(agentsession.Reader)
	if !ok {
		return nil, fmt.Errorf("native: store %T cannot read a session without holding it", r.b.rec.Store())
	}
	return rd.Read(ctx, id)
}

// Refs lists the refs whose target is the session.
func (r record) Refs(ctx context.Context, id string) ([]agentsession.Ref, error) {
	if id == "" {
		id = r.b.rec.SessionID()
	}
	rs, ok := r.b.rec.Store().(agentsession.RefStore)
	if !ok {
		return nil, agentsession.ErrNoRefs
	}
	var out []agentsession.Ref
	for ref, err := range rs.ListRefs(ctx, "") {
		if err != nil {
			return nil, err
		}
		if ref.Target.Session == id {
			out = append(out, ref)
		}
	}
	return out, nil
}

// Live subscribes to the agent when it is called, not when the result is
// first ranged over, so a client that calls it and then starts a run
// misses nothing. The subscription ends when ctx is done. Events queue
// without bound until the client takes them: the agent's delivery is a
// barrier, and a slow client must not stall the run or the recorder.
func (b *Backend) Live(ctx context.Context) iter.Seq2[client.LiveEvent, error] {
	q := &queue{wake: make(chan struct{}, 1)}
	unsub := b.agent.Subscribe(func(_ context.Context, ev agentturn.Event) error {
		if le, ok := client.FromEvent(ev); ok {
			q.push(le)
		}
		return nil
	})
	stop := context.AfterFunc(ctx, unsub)
	return func(yield func(client.LiveEvent, error) bool) {
		defer func() {
			stop()
			unsub()
		}()
		for {
			batch := q.take()
			for _, le := range batch {
				if !yield(le, nil) {
					return
				}
			}
			if len(batch) > 0 {
				continue
			}
			select {
			case <-q.wake:
			case <-ctx.Done():
				if !errors.Is(ctx.Err(), context.Canceled) {
					yield(nil, ctx.Err())
				}
				return
			}
		}
	}
}

// queue is an unbounded FIFO with a wake-up for its one reader.
type queue struct {
	mu   sync.Mutex
	evs  []client.LiveEvent
	wake chan struct{}
}

func (q *queue) push(ev client.LiveEvent) {
	q.mu.Lock()
	q.evs = append(q.evs, ev)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *queue) take() []client.LiveEvent {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.evs
	q.evs = nil
	return out
}
