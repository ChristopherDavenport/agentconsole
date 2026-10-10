package client

import (
	"context"
	"errors"
	"iter"
	"slices"
	"sync"

	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"
)

// Live subscribes to c when it is called, not when the result is first
// ranged over, so a client that calls it and then starts a run misses
// nothing, and yields what the view reads of each event, copied
// ([Copy]) inside the subscriber. Past events are not replayed, except
// the questions still waiting, which [agentturn.Control.Subscribe] sends
// a new subscriber first; a run already going is read from c's State.
//
// Events queue without bound until the caller takes them: an in-process
// agent's delivery is a barrier, and a slow client must not stall the
// run or the recorder. The subscription ends when ctx is done or the
// caller stops ranging.
func Live(ctx context.Context, c agentturn.Control) iter.Seq2[agentturn.Event, error] {
	q := &queue{wake: make(chan struct{}, 1)}
	unsub := c.Subscribe(func(_ context.Context, ev agentturn.Event) error {
		if ev, ok := Copy(ev); ok {
			q.push(ev)
		}
		return nil
	})
	stop := context.AfterFunc(ctx, unsub)
	return func(yield func(agentturn.Event, error) bool) {
		defer func() {
			stop()
			unsub()
		}()
		for {
			batch := q.take()
			for _, ev := range batch {
				if !yield(ev, nil) {
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
	evs  []agentturn.Event
	wake chan struct{}
}

func (q *queue) push(ev agentturn.Event) {
	q.mu.Lock()
	q.evs = append(q.evs, ev)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *queue) take() []agentturn.Event {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.evs
	q.evs = nil
	return out
}

// Copy returns what a view keeps of ev, sharing nothing the agent goes
// on changing, and false for an event a view does not read: an item
// the model never sees (Hidden), a response end without its response,
// and the events of the loop's own bookkeeping (turn_end, queued,
// model_blocked). An event's item is the agent's accumulator, so a
// subscriber copies it before its callback returns, as [Live] does.
//
// What the view does not read and could be large is left out of the
// copy: a run end's transcript, a request's input (only its model is
// kept), an update's raw stream event, a response's output items (its
// ID, model, status and usage are kept), and a pending call's tool.
func Copy(ev agentturn.Event) (agentturn.Event, bool) {
	switch e := ev.(type) {
	case *agentturn.RunStart:
		c := *e
		return &c, true
	case *agentturn.TurnStart:
		c := *e
		c.Request = openresponses.Request{Model: e.Request.Model}
		c.Inputs = e.Inputs.Clone()
		c.Arrived = slices.Clone(e.Arrived)
		return &c, true
	case *agentturn.ModelRetry:
		c := *e
		c.Request = openresponses.Request{Model: e.Request.Model}
		return &c, true
	case *agentturn.ItemStart:
		if e.Hidden {
			return nil, false
		}
		c := *e
		c.Item = CloneItem(e.Item)
		return &c, true
	case *agentturn.ItemUpdate:
		c := *e
		c.Item, c.Stream = CloneItem(e.Item), nil
		return &c, true
	case *agentturn.ItemEnd:
		if e.Hidden {
			return nil, false
		}
		c := *e
		c.Item = CloneItem(e.Item)
		return &c, true
	case *agentturn.ResponseEnd:
		if e.Response == nil {
			return nil, false
		}
		c := *e
		r := e.Response
		c.Response = &openresponses.Response{ID: r.ID, Model: r.Model, Status: r.Status}
		if r.Usage != nil {
			u := *r.Usage
			c.Response.Usage = &u
		}
		return &c, true
	case *agentturn.ToolStart:
		c := *e
		c.Args = slices.Clone(e.Args)
		c.Decision = copyDecision(e.Decision)
		return &c, true
	case *agentturn.ToolDispatch:
		c := *e
		return &c, true
	case *agentturn.ToolUpdate:
		c := *e
		return &c, true
	case *agentturn.ToolEnd:
		c := *e
		return &c, true
	case *agentturn.RunEnd:
		c := *e
		c.Items = nil
		c.Pending = make([]agentturn.PendingCall, 0, len(e.Pending))
		for _, p := range e.Pending {
			p.Tool = nil
			if p.Call != nil {
				p.Call = CloneItem(p.Call).(*openresponses.FunctionCall)
			}
			if p.Ran != nil {
				p.Ran = CloneItem(p.Ran).(*openresponses.FunctionCallOutput)
			}
			p.Args = slices.Clone(p.Args)
			p.Decision = copyDecision(p.Decision)
			c.Pending = append(c.Pending, p)
		}
		return &c, true
	case *agentturn.Question:
		c := *e
		if e.Call != nil {
			call := *e.Call
			call.Args = slices.Clone(call.Args)
			call.Decision = copyDecision(call.Decision)
			c.Call = &call
		}
		return &c, true
	case *agentturn.QuestionClosed:
		c := *e
		if e.Answer != nil {
			a := *e.Answer
			c.Answer = &a
		}
		return &c, true
	}
	return nil, false
}

func copyDecision(d *agentturn.ToolDecision) *agentturn.ToolDecision {
	if d == nil {
		return nil
	}
	c := *d
	c.Args = slices.Clone(d.Args)
	return &c
}

// CloneItem returns a deep copy of item, which shares nothing with it.
func CloneItem(item openresponses.Item) openresponses.Item {
	if item == nil {
		return nil
	}
	return openresponses.Items{item}.Clone()[0]
}

// ItemID is the item's own ID, "" for an item that has none.
func ItemID(item openresponses.Item) string {
	switch v := item.(type) {
	case *openresponses.Message:
		return v.ID
	case *openresponses.ReasoningItem:
		return v.ID
	case *openresponses.FunctionCall:
		return v.ID
	case *openresponses.FunctionCallOutput:
		return v.ID
	case *openresponses.Compaction:
		return v.ID
	}
	return ""
}

// CallID is the call ID of a function call or its output, "" for any
// other item.
func CallID(item openresponses.Item) string {
	switch v := item.(type) {
	case *openresponses.FunctionCall:
		return v.CallID
	case *openresponses.FunctionCallOutput:
		return v.CallID
	}
	return ""
}
