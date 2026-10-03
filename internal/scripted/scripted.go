// Package scripted is a scripted model for the tests that run a real
// agent: a Streamer that answers each request with the next of its
// steps, built on the emitter, offline and deterministic.
package scripted

import (
	"context"
	"fmt"
	"sync"

	"github.com/ChristopherDavenport/openresponses"
)

// Step is one model response.
type Step func(ctx context.Context, em *openresponses.Emitter) error

// Model answers request i with step i, and fails a request past the
// last. It also keeps the requests it saw.
type Model struct {
	mu       sync.Mutex
	steps    []Step
	n        int
	requests []openresponses.Request
}

// New returns a model that plays steps in order.
func New(steps ...Step) *Model { return &Model{steps: steps} }

// Requests returns the requests the model has received.
func (m *Model) Requests() []openresponses.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]openresponses.Request(nil), m.requests...)
}

// CreateStream implements openresponses.Streamer.
func (m *Model) CreateStream(ctx context.Context, req openresponses.Request, sink openresponses.EventSink) error {
	m.mu.Lock()
	i := m.n
	m.n++
	m.requests = append(m.requests, req)
	m.mu.Unlock()
	if i >= len(m.steps) {
		return fmt.Errorf("scripted: request %d, only %d steps", i+1, len(m.steps))
	}
	em := openresponses.NewEmitter(sink, openresponses.NewResponse(req))
	if err := m.steps[i](ctx, em); err != nil {
		return err
	}
	return em.Complete()
}

// Say streams a final-answer message in chunks.
func Say(chunks ...string) Step {
	return func(_ context.Context, em *openresponses.Emitter) error {
		w, err := em.Message(openresponses.PhaseFinalAnswer)
		if err != nil {
			return err
		}
		for _, c := range chunks {
			if err := w.Text(c); err != nil {
				return err
			}
		}
		return w.Close()
	}
}

// Call streams one function call.
func Call(callID, name, args string) Step {
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

// SayThenBlock streams one chunk and then waits for the request's
// context to end, as a model that never finishes does.
func SayThenBlock(chunk string) Step {
	return func(ctx context.Context, em *openresponses.Emitter) error {
		w, err := em.Message(openresponses.PhaseFinalAnswer)
		if err != nil {
			return err
		}
		if err := w.Text(chunk); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}
}
