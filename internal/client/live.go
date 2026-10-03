package client

import (
	"time"

	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"
)

// LiveEvent is a narrowed view of [agentturn.Event]: the events that
// say something the record does not hold yet. Every value is a copy that
// shares nothing with the agent, so a client may keep it. Concrete types
// are [RunStarted], [TurnStarted], [ModelRetrying], [ItemOpened],
// [ItemUpdated], [ItemCompleted], [ResponseCompleted], [ToolOpened],
// [ToolDispatched], [ToolProgress], [ToolFinished] and [RunEnded]; all
// are pointers.
type LiveEvent interface {
	liveEvent()
	// Run is the ID of the run the event belongs to.
	Run() string
}

// RunStarted opens a run. Resume is set for a run that answers pending
// calls.
type RunStarted struct {
	RunID  string
	Resume bool
}

// TurnStarted opens a model call.
type TurnStarted struct {
	RunID string
	Turn  int
}

// ModelRetrying says the model call failed and will be tried again.
// What the failed attempt streamed is dropped, so a client drops what
// it showed of that turn's open items.
type ModelRetrying struct {
	RunID   string
	Turn    int
	Attempt int
	Err     error
	Delay   time.Duration
}

// ItemOpened announces an item the model has begun, or one the loop
// appended. ResponseID is empty while the stream has not named its
// response.
type ItemOpened struct {
	RunID      string
	ResponseID string
	Item       openresponses.Item
}

// ItemUpdated carries the item as accumulated so far.
type ItemUpdated struct {
	RunID      string
	ResponseID string
	Item       openresponses.Item
}

// ItemCompleted carries a finished item. It is not a commit: the record
// writes the item when its entry lands, which for a model's item that
// completed before its response was named is at the response's end.
type ItemCompleted struct {
	RunID      string
	ResponseID string
	Item       openresponses.Item
}

// ResponseCompleted carries the model's response, whose ID names the
// items that completed before the stream did.
type ResponseCompleted struct {
	RunID      string
	ResponseID string
	Model      string
	Status     openresponses.ResponseStatus
}

// ToolOpened announces a call the loop is about to settle. Parent is the
// ID of the call whose tool made this one, empty for the model's.
type ToolOpened struct {
	RunID  string
	CallID string
	Name   string
	Args   string
	Parent string
}

// ToolDispatched says the call reached its tool.
type ToolDispatched struct {
	RunID  string
	CallID string
	Name   string
}

// ToolProgress is a partial result from a running tool.
type ToolProgress struct {
	RunID   string
	CallID  string
	Name    string
	Partial string
}

// ToolFinished says a call ended. Deferred means nothing ran and the
// call waits for an answer: a permission request, with Reason as the
// question. Blocked means a policy refused it. Result is the output the
// model sees, empty for a deferred call; it is in the record once the
// output item lands.
type ToolFinished struct {
	RunID    string
	CallID   string
	Name     string
	Result   string
	Err      error
	Blocked  bool
	Deferred bool
	Reason   string
	Parent   string
}

// Pending is a call that waits for the caller.
type Pending struct {
	CallID string
	Name   string
	Args   string
	// Reason says why it waits: deferred to the caller, cut off by an
	// abort, and so on.
	Reason agentturn.PendingReason
}

// RunEnded closes a run. Pending lists the calls the run left waiting,
// which is the authoritative permission list for Reason input_required.
type RunEnded struct {
	RunID   string
	Reason  agentturn.Reason
	Err     error
	Pending []Pending
}

func (*RunStarted) liveEvent()        {}
func (*TurnStarted) liveEvent()       {}
func (*ModelRetrying) liveEvent()     {}
func (*ItemOpened) liveEvent()        {}
func (*ItemUpdated) liveEvent()       {}
func (*ItemCompleted) liveEvent()     {}
func (*ResponseCompleted) liveEvent() {}
func (*ToolOpened) liveEvent()        {}
func (*ToolDispatched) liveEvent()    {}
func (*ToolProgress) liveEvent()      {}
func (*ToolFinished) liveEvent()      {}
func (*RunEnded) liveEvent()          {}

// Run implements [LiveEvent].
func (e *RunStarted) Run() string        { return e.RunID }
func (e *TurnStarted) Run() string       { return e.RunID }
func (e *ModelRetrying) Run() string     { return e.RunID }
func (e *ItemOpened) Run() string        { return e.RunID }
func (e *ItemUpdated) Run() string       { return e.RunID }
func (e *ItemCompleted) Run() string     { return e.RunID }
func (e *ResponseCompleted) Run() string { return e.RunID }
func (e *ToolOpened) Run() string        { return e.RunID }
func (e *ToolDispatched) Run() string    { return e.RunID }
func (e *ToolProgress) Run() string      { return e.RunID }
func (e *ToolFinished) Run() string      { return e.RunID }
func (e *RunEnded) Run() string          { return e.RunID }

// FromEvent narrows an agent event to a live one. It reports false for an
// event that carries nothing the record lacks or that a client does not
// render: turn_end, queued, model_blocked. A hidden item is dropped too,
// since a renderer shows none.
//
// An event's item is the agent's accumulator and keeps changing as the
// stream goes on, so FromEvent copies it. It is called from the agent's
// subscriber, which the agent delivers as a barrier, so nothing mutates
// the item during the copy.
func FromEvent(ev agentturn.Event) (LiveEvent, bool) {
	switch e := ev.(type) {
	case *agentturn.RunStart:
		return &RunStarted{RunID: e.RunID, Resume: e.Source == agentturn.SourceResume}, true
	case *agentturn.TurnStart:
		return &TurnStarted{RunID: e.RunID, Turn: e.Turn}, true
	case *agentturn.ModelRetry:
		return &ModelRetrying{RunID: e.RunID, Turn: e.Turn, Attempt: e.Attempt, Err: e.Err, Delay: e.Delay}, true
	case *agentturn.ItemStart:
		if e.Hidden {
			return nil, false
		}
		return &ItemOpened{RunID: e.RunID, ResponseID: e.ResponseID, Item: CloneItem(e.Item)}, true
	case *agentturn.ItemUpdate:
		return &ItemUpdated{RunID: e.RunID, ResponseID: e.ResponseID, Item: CloneItem(e.Item)}, true
	case *agentturn.ItemEnd:
		if e.Hidden {
			return nil, false
		}
		return &ItemCompleted{RunID: e.RunID, ResponseID: e.ResponseID, Item: CloneItem(e.Item)}, true
	case *agentturn.ResponseEnd:
		if e.Response == nil {
			return nil, false
		}
		return &ResponseCompleted{RunID: e.RunID, ResponseID: e.Response.ID, Model: e.Response.Model, Status: e.Response.Status}, true
	case *agentturn.ToolStart:
		return &ToolOpened{RunID: e.RunID, CallID: e.CallID, Name: e.Name, Args: string(e.Args), Parent: e.Parent}, true
	case *agentturn.ToolDispatch:
		return &ToolDispatched{RunID: e.RunID, CallID: e.CallID, Name: e.Name}, true
	case *agentturn.ToolUpdate:
		return &ToolProgress{RunID: e.RunID, CallID: e.CallID, Name: e.Name, Partial: resultText(e.Partial)}, true
	case *agentturn.ToolEnd:
		return &ToolFinished{RunID: e.RunID, CallID: e.CallID, Name: e.Name, Result: resultText(e.Result),
			Err: e.Err, Blocked: e.Blocked, Deferred: e.Deferred, Reason: e.Reason, Parent: e.Parent}, true
	case *agentturn.RunEnd:
		end := &RunEnded{RunID: e.RunID, Reason: e.Reason, Err: e.Err}
		for _, p := range e.Pending {
			if p.Call == nil {
				continue
			}
			end.Pending = append(end.Pending, Pending{CallID: p.Call.CallID, Name: p.Call.Name, Args: p.Call.Arguments, Reason: p.Reason})
		}
		return end, true
	}
	return nil, false
}

func resultText(r agenttool.Result) string { return r.Output.Text }

// CloneItem returns a copy of item that shares nothing with it.
func CloneItem(item openresponses.Item) openresponses.Item {
	if item == nil {
		return nil
	}
	return openresponses.Items{item}.Clone()[0]
}

// ItemID is the ID an item carries, and "" for one with none. A function
// call or its output is keyed by its call ID instead, which [CallID]
// reads.
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

// CallID is the call ID of a function call or its output, and "" for any
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
