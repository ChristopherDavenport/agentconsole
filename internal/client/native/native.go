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

	"github.com/ChristopherDavenport/agentconsole/internal/client"
)

// Backend drives one agent and follows the session its recorder writes.
type Backend struct {
	agent *agentturn.Agent
	rec   *session.Recorder
	store agentsession.Follower
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
func (b *Backend) Control() client.Control { return control{b.agent} }

// Record implements [client.Backend].
func (b *Backend) Record() client.Record { return record{b} }

type control struct{ a *agentturn.Agent }

func (c control) Prompt(ctx context.Context, items ...openresponses.Item) error {
	_, err := c.a.Prompt(ctx, items...)
	return err
}

func (c control) Steer(items ...openresponses.Item) { c.a.Steer(items...) }

func (c control) Abort() { c.a.Abort() }

func (c control) Answer(ctx context.Context, answers ...agentturn.Answer) error {
	_, err := c.a.Resume(ctx, answers...)
	return err
}

func (c control) State() agentturn.State { return c.a.State() }

type record struct{ b *Backend }

func (r record) Follow(ctx context.Context, from agentsession.Cursor) iter.Seq2[agentsession.Change, error] {
	return r.b.store.Follow(ctx, r.b.rec.SessionID(), from)
}

// Verified is true: the record is the agent's own, written by its
// recorder with request hashes a reader can check.
func (record) Verified() bool { return true }

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
