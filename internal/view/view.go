// Package view is the reconciler: a pure model that takes the record's
// changes and the backend's live events, in whatever order they arrive,
// and produces what a client renders.
//
// The rule is the plan's: the record is the truth for everything
// committed, and live events cover only what is not. So a rendering is
// the path's items from the record, then an overlay of what is in flight
// on top: items still streaming, calls opened and not answered, the state
// of the turn, the permissions that are out. An overlay item is dropped
// when the entry carrying it lands, matched on the item's ID within its
// response, or on the call ID for a function call and its output.
//
// # Order independence
//
// The record and the live stream reach a client through two paths, and
// an entry is written before the live event that follows it is queued,
// so a follower can deliver the entry first. The view therefore remembers
// what has landed and ignores a live event for an item that has: an entry
// never leaves a ghost behind it. A landed item is shown once, from the
// record; an overlay item is shown once, until its entry lands.
//
// # The recorder's lags
//
// The overlay waits for the entry, never for the event.
//   - A live item_end is not a commit. A model item that completes
//     before the stream names its response is written at response_end,
//     so its overlay copy stays through [client.ItemCompleted] and
//     [client.ResponseCompleted] until the entry lands.
//   - A config entry is written by the next entry-writing event, so the
//     record's model lags the model of the turn running. Both are
//     reported: [Model.Config] is the record's, [Turn.Model] the
//     turn's.
//   - A run's end flushes what is left of its overlay, but only once the
//     record holds the run's end entry as well, which is written after
//     everything the run wrote. Flushing at the live event alone would
//     drop an item whose entry the follower has not delivered yet.
//
// # Limits
//
// An item with no ID, which only the loop appends (a prompt, a steered
// message), has no key to reconcile on and is never overlaid: the
// recorder writes it in the barrier of its item_end, so it is on the
// record before the model is called. A function call output is not
// overlaid either; the call row carries the result until the output
// lands. A model that streams items with no ID would not show them live.
//
// A View is not safe for concurrent use; a client drives it from one
// goroutine.
package view

import (
	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/internal/client"
)

// TurnState says what the agent is doing.
type TurnState int

// Turn states.
const (
	// Idle: no run is going and nothing waits.
	Idle TurnState = iota
	// Running: a run is going.
	Running
	// RequiresAction: the last run ended with calls waiting for an
	// answer, which [Model.Permissions] lists.
	RequiresAction
)

// String names the state.
func (s TurnState) String() string {
	switch s {
	case Idle:
		return "idle"
	case Running:
		return "running"
	case RequiresAction:
		return "requires_action"
	}
	return "unknown"
}

// Turn is the state of the run, from live events alone.
type Turn struct {
	State TurnState
	RunID string
	// Number is the turn within the run, from 1, and 0 between runs.
	Number int
	// Model is the model the turn is calling, which can be ahead of
	// [Model.Config]: the config entry is written by the next
	// entry-writing event.
	Model string
	// Withheld is set when the last run ended because a guard withheld
	// a message: the text that streamed was never committed.
	Withheld bool
	// Attempt is the number of the model call that failed and is being
	// retried, 0 when none is.
	Attempt int
}

// CallState says how far a call got.
type CallState int

// Call states.
const (
	// CallOpen: the call is on the record or the stream and has not
	// reached its tool.
	CallOpen CallState = iota
	// CallRunning: the call was handed to its tool.
	CallRunning
	// CallEnded: the call ended and Output holds what the model sees.
	CallEnded
	// CallBlocked: a policy refused the call; Output says why.
	CallBlocked
	// CallDeferred: the call waits for an answer.
	CallDeferred
	// CallCutOff: an abort or a failure ended the run with the call
	// unanswered. It is not a question; the next prompt answers it.
	CallCutOff
)

// String names the state.
func (s CallState) String() string {
	switch s {
	case CallOpen:
		return "open"
	case CallRunning:
		return "running"
	case CallEnded:
		return "ended"
	case CallBlocked:
		return "blocked"
	case CallDeferred:
		return "deferred"
	case CallCutOff:
		return "cut_off"
	}
	return "unknown"
}

// Call is how a function call stands.
type Call struct {
	CallID string
	Name   string
	Args   string
	State  CallState
	// Partial is the latest progress of a running tool.
	Partial string
	// Output is what the model sees, once the call has ended.
	Output string
	// Committed is set when the output is on the record, so the call is
	// answered for good; before that Output is live.
	Committed bool
	// Verdict and Reason are the last policy decision the record holds
	// for the call, and Reason the question of a deferred one.
	Verdict string
	Reason  string
	// Children are the calls this call's tool made, live only.
	Children []Call
}

// Row is one line of the conversation.
type Row struct {
	// EntryID is the entry that holds the item, and "" while the item is
	// live only.
	EntryID string
	// Live is set for an overlay row: not on the record yet.
	Live bool
	// Open is set for a live row still streaming.
	Open bool
	// KeptFromModel marks a row the record holds as a custom entry: an
	// item the filter kept out of the model's context. It is on the
	// record, so it is shown, and a renderer may say it was not sent.
	KeptFromModel bool
	ResponseID    string
	Item          openresponses.Item
	// Call is set on the row of a function call.
	Call *Call
}

// Permission is a call that waits for the caller.
type Permission struct {
	CallID string
	Name   string
	Args   string
	// Question is the reason the call was deferred, as a front puts it
	// to the user.
	Question string
	Reason   string
}

// Model is what a client renders.
type Model struct {
	// Session is the followed session's ID.
	Session string
	// Leaf is the entry the rendered path ends at, and Leaves every
	// branch tip the session has.
	Leaf   string
	Leaves []string
	// Config is the model in force at the leaf, by the record.
	Config string
	// Rows is the path's visible items, then the live rows.
	Rows        []Row
	Turn        Turn
	Permissions []Permission
	// CutOff lists the calls the last run left unanswered without asking
	// anyone: it was aborted or failed. They are shown as cut off.
	CutOff []Permission
}

type ovItem struct {
	run        string
	turn       int
	key        key
	responseID string
	item       openresponses.Item
	done       bool
}

type ovCall struct {
	run      string
	callID   string
	name     string
	args     string
	parent   string
	state    CallState
	partial  string
	output   string
	reason   string
	finished bool
}

// maxEndSeen caps the run end entries remembered.
const maxEndSeen = 64

// key identifies an item across the live stream and the record.
type key struct {
	call bool // a function call, keyed by call ID
	id   string
}

// View reconciles the record with the live stream.
type View struct {
	session string
	leaf    string
	leaves  []string
	path    []agentsession.Entry

	// What the record holds, by key: every entry that has landed on any
	// branch.
	landedItems map[string][]string // item ID to the response IDs it landed under
	landedCalls map[string]bool     // function call items, by call ID
	landedOuts  map[string]bool     // function call outputs, by call ID
	// settled are the responses known to be over: the live stream ended
	// them, or the record held their response entry when it was read. An
	// item with no response ID yet cannot belong to one.
	settled map[string]bool

	items []*ovItem
	calls []*ovCall
	perms []Permission
	cut   []Permission
	// liveEnded are the runs whose live end was seen and whose overlay
	// waits for the record; endSeen the end entries the record has
	// delivered, capped, since a run the live stream never mentions
	// (another process's) is never settled by it.
	liveEnded map[string]bool
	endSeen   map[string]bool
	endOrder  []string
	turn      Turn
}

// New returns an empty view.
func New() *View {
	return &View{
		landedItems: map[string][]string{},
		landedCalls: map[string]bool{},
		landedOuts:  map[string]bool{},
		settled:     map[string]bool{},
		liveEnded:   map[string]bool{},
		endSeen:     map[string]bool{},
	}
}

// Record applies a change from the record's follow.
func (v *View) Record(ch agentsession.Change) {
	switch ch.Kind {
	case agentsession.Snapshot, agentsession.Reset:
		v.rebuild(ch.Session)
		v.syncPending(ch.Session)
	case agentsession.Appended:
		v.setSession(ch.Session, "")
		v.land(ch.Entry)
		if r, ok := ch.Entry.(*agentsession.RunEntry); ok && r.IsEnd() {
			v.syncPending(ch.Session)
		}
	case agentsession.Head:
		v.setSession(ch.Session, ch.Leaf)
		v.syncPending(ch.Session)
	}
}

// syncPending reads what waits on an answer from the record: the calls a
// run's end entry lists as pending that the path still holds without an
// output. A view attached to a session that stopped at input_required
// shows its permissions from here, with no live event, and the live
// events refine them (the question of a call, a later run). The record's
// last run speaks only when the live stream has said nothing of a later
// one, and never while a run is going.
func (v *View) syncPending(s *agentsession.Session) {
	if v.turn.State == Running {
		return
	}
	var last *agentsession.RunEntry
	for i := len(v.path) - 1; i >= 0 && last == nil; i-- {
		if r, ok := v.path[i].(*agentsession.RunEntry); ok {
			last = r
		}
	}
	if last == nil || !last.IsEnd() || (v.turn.RunID != "" && v.turn.RunID != last.RunID) {
		return
	}
	waiting := map[string]bool{}
	for _, id := range last.Pending {
		waiting[id] = true
	}
	old := v.perms
	v.perms, v.cut = nil, nil
	calls, err := s.PendingCalls(v.path[len(v.path)-1].Base().ID)
	if err == nil {
		for _, c := range calls {
			if !waiting[c.ID()] {
				continue
			}
			p := Permission{CallID: c.ID(), Name: c.Call.Name, Args: c.Call.Arguments}
			known := false
			for _, o := range old {
				known = known || o.CallID == p.CallID
			}
			if last.Reason == agentsession.ReasonInputRequired && (c.Held() || known) {
				p.Reason = string(agentturn.PendingDeferred)
				if n := len(c.Decisions); n > 0 {
					p.Question = c.Decisions[n-1].Reason
				}
				for _, o := range old {
					if o.CallID == p.CallID && o.Question != "" {
						p.Question = o.Question
					}
				}
				v.perms = append(v.perms, p)
				continue
			}
			p.Reason = last.Reason
			v.cut = append(v.cut, p)
		}
	}
	v.turn.RunID = last.RunID
	v.turn.State = Idle
	if len(v.perms) > 0 {
		v.turn.State = RequiresAction
	}
}

// rebuild starts from a session read whole: what the view derived from
// the record before is dropped, and the overlay is pruned against what the
// session holds, so an overlay item the new session has is not shown
// twice and one it lacks stays until its entry lands.
func (v *View) rebuild(s *agentsession.Session) {
	v.landedItems = map[string][]string{}
	v.landedCalls = map[string]bool{}
	v.landedOuts = map[string]bool{}
	v.endSeen, v.endOrder = map[string]bool{}, nil
	v.setSession(s, "")
	for _, e := range s.Entries() {
		v.land(e)
		if r, ok := e.(*agentsession.ResponseEntry); ok {
			v.settled[r.ResponseID] = true
		}
	}
}

func (v *View) setSession(s *agentsession.Session, leaf string) {
	if s == nil {
		return
	}
	v.session = s.ID()
	if leaf == "" {
		leaf = s.Leaf()
	}
	v.leaf = resting(s, leaf)
	// The session is the follower's own and valid until the next step,
	// so the path and the leaves are copied out.
	v.path = append([]agentsession.Entry(nil), s.Path(lineEnd(s, leaf))...)
	v.leaves = tips(s)
}

// resting reads an entry as the item its branch rests on. A run end, a
// response and a label are bookkeeping that follows the content: each
// stands for its parent, so a bookmark appended last does not hide the
// tip it was appended behind.
func resting(s *agentsession.Session, id string) string {
	for hops := 0; hops <= s.Len(); hops++ {
		e, ok := s.Entry(id)
		if !ok {
			break
		}
		if _, isItem := e.(*agentsession.ItemEntry); isItem || e.Base().Parent == "" {
			break
		}
		id = e.Base().Parent
	}
	return id
}

// lineEnd walks from id down through the bookkeeping that follows an item
// (the decisions, dispatches, response and run entries a branch ends
// with) to the last of it, stopping where the next entry is an item. A
// leaf rests on an item, and the entries behind it still say what became
// of its calls and how its run ended.
func lineEnd(s *agentsession.Session, id string) string {
	for hops := 0; hops <= s.Len(); hops++ {
		next := ""
		for _, c := range s.Children(id) {
			if e, ok := s.Entry(c); ok {
				if _, isItem := e.(*agentsession.ItemEntry); !isItem {
					next = c
				}
			}
		}
		if next == "" {
			return id
		}
		id = next
	}
	return id
}

// tips are the branch tips of the session, one per branch: the childless
// entries Leaves lists, each read as the item it rests on, and branches
// that end on one item counted once. Session.Branches upstream would
// replace this (agentsession#197).
func tips(s *agentsession.Session) []string {
	var out []string
	seen := map[string]bool{}
	for _, id := range s.Leaves() {
		id = resting(s, id)
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// land records an entry that has landed and drops what it replaces.
func (v *View) land(e agentsession.Entry) {
	switch x := e.(type) {
	case *agentsession.ItemEntry:
		v.landItem(x.Item, x.ResponseID)
	case *agentsession.CustomEntry:
		// An item the filter keeps from the model is written as a custom
		// entry, which is still the item landing.
		if item, resp, ok := session.MarkedItem(x); ok {
			v.landItem(item, resp)
		}
	case *agentsession.RunEntry:
		if x.IsEnd() {
			v.noteEnd(x.RunID)
			v.flushIfSettled(x.RunID)
			return
		}
		// A run's start lands behind everything the runs before it
		// wrote, so a run that ended live and wrote no end entry is
		// settled by it.
		for id := range v.liveEnded {
			if id != x.RunID {
				v.flush(id)
				delete(v.liveEnded, id)
				delete(v.endSeen, id)
			}
		}
	}
}

func (v *View) noteEnd(id string) {
	if v.endSeen[id] {
		return
	}
	v.endSeen[id] = true
	v.endOrder = append(v.endOrder, id)
	for len(v.endOrder) > maxEndSeen {
		delete(v.endSeen, v.endOrder[0])
		v.endOrder = v.endOrder[1:]
	}
}

func (v *View) landItem(item openresponses.Item, responseID string) {
	switch x := item.(type) {
	case *openresponses.FunctionCall:
		v.landedCalls[x.CallID] = true
		v.dropItems(func(o *ovItem) bool { return o.key.call && o.key.id == x.CallID })
	case *openresponses.FunctionCallOutput:
		v.landedOuts[x.CallID] = true
		v.dropCall(x.CallID)
	default:
		id := client.ItemID(item)
		if id == "" {
			return
		}
		v.landedItems[id] = append(v.landedItems[id], responseID)
		v.dropItems(func(o *ovItem) bool { return !o.key.call && o.key.id == id && v.same(o.responseID, responseID) })
	}
}

// same reports whether an overlay item of response overlay is the item an
// entry of response entry holds. Response IDs are compared strictly. The
// one case that is not an equality is an overlay row whose stream has not
// named its response yet: it is the entry's if that response is still
// open, since a response that is over cannot be the one the row is in.
func (v *View) same(overlay, entry string) bool {
	if overlay == entry {
		return true
	}
	return overlay == "" && entry != "" && !v.settled[entry]
}

// sameTurn reports whether two rows of one run and turn can be one item:
// their response IDs agree, or one of them is not known yet. The run and
// turn identify a row until the stream names its response.
func sameTurn(a, b string) bool { return a == b || a == "" || b == "" }

func (v *View) landed(k key, responseID string) bool {
	if k.call {
		return v.landedCalls[k.id]
	}
	for _, r := range v.landedItems[k.id] {
		if v.same(responseID, r) {
			return true
		}
	}
	return false
}

func (v *View) dropItems(match func(*ovItem) bool) {
	kept := v.items[:0]
	for _, o := range v.items {
		if !match(o) {
			kept = append(kept, o)
		}
	}
	clear(v.items[len(kept):])
	v.items = kept
}

// dropCall ends the overlay of a call whose output has landed: its state,
// the calls its tool made, and the permission that asked about it.
func (v *View) dropCall(callID string) {
	kept := v.calls[:0]
	for _, c := range v.calls {
		if c.callID != callID && c.parent != callID {
			kept = append(kept, c)
		}
	}
	clear(v.calls[len(kept):])
	v.calls = kept
	perms := v.perms[:0]
	for _, p := range v.perms {
		if p.CallID != callID {
			perms = append(perms, p)
		}
	}
	v.perms = perms
	cut := v.cut[:0]
	for _, p := range v.cut {
		if p.CallID != callID {
			cut = append(cut, p)
		}
	}
	v.cut = cut
	if len(v.perms) == 0 && v.turn.State == RequiresAction {
		v.turn.State = Idle
	}
}

// flushIfSettled flushes a run's leftovers once both halves of its end
// are in: the live event and the record's end entry.
func (v *View) flushIfSettled(runID string) {
	if !v.liveEnded[runID] || !v.endSeen[runID] {
		return
	}
	v.flush(runID)
	delete(v.liveEnded, runID)
	delete(v.endSeen, runID)
}

// flush drops what a finished run left in the overlay, except the calls
// that still wait for an answer.
func (v *View) flush(runID string) {
	v.dropItems(func(o *ovItem) bool { return o.run == runID })
	waiting := map[string]bool{}
	for _, p := range v.perms {
		waiting[p.CallID] = true
	}
	kept := v.calls[:0]
	for _, c := range v.calls {
		if c.run != runID || waiting[c.callID] || waiting[c.parent] {
			kept = append(kept, c)
		}
	}
	clear(v.calls[len(kept):])
	v.calls = kept
}

// Live applies a live event.
func (v *View) Live(ev client.LiveEvent) {
	switch e := ev.(type) {
	case *client.RunStarted:
		// A run that starts has answered every call that waited, since
		// the agent refuses it otherwise.
		v.perms, v.cut = nil, nil
		kept := v.calls[:0]
		for _, c := range v.calls {
			if c.state != CallDeferred {
				kept = append(kept, c)
			}
		}
		clear(v.calls[len(kept):])
		v.calls = kept
		v.turn = Turn{State: Running, RunID: e.RunID}
	case *client.TurnStarted:
		v.turn.Number, v.turn.Attempt = e.Turn, 0
		v.turn.Model = e.Model
	case *client.ModelRetrying:
		// The failed attempt's items are dropped with it.
		v.turn.Attempt = e.Attempt
		v.dropItems(func(o *ovItem) bool { return o.run == e.RunID && o.turn == e.Turn })
	case *client.ItemOpened:
		v.item(e.RunID, e.ResponseID, e.Item, false)
	case *client.ItemUpdated:
		v.item(e.RunID, e.ResponseID, e.Item, false)
	case *client.ItemCompleted:
		v.item(e.RunID, e.ResponseID, e.Item, true)
	case *client.ResponseCompleted:
		// The response names the items that completed before the stream
		// did, and is over: an item with no response yet is not its.
		if e.ResponseID != "" {
			v.settled[e.ResponseID] = true
			for _, o := range v.items {
				if o.run == e.RunID && o.turn == v.turn.Number && o.responseID == "" {
					o.responseID = e.ResponseID
				}
			}
			// One whose entry landed first is the entry's.
			v.dropItems(func(o *ovItem) bool {
				if o.key.call || o.responseID != e.ResponseID {
					return false
				}
				for _, r := range v.landedItems[o.key.id] {
					if r == e.ResponseID {
						return true
					}
				}
				return false
			})
		}
		// An item still streaming when its response ends never got an
		// item_end, so the record will not hold it: a message the guard
		// withheld, or one a failed response cut off.
		v.dropItems(func(o *ovItem) bool { return o.run == e.RunID && o.turn == v.turn.Number && !o.done })
	case *client.ToolOpened:
		if v.landedOuts[e.CallID] {
			return
		}
		c := v.call(e.RunID, e.CallID)
		c.name, c.args, c.parent = e.Name, e.Args, e.Parent
	case *client.ToolDispatched:
		if v.landedOuts[e.CallID] {
			return
		}
		c := v.call(e.RunID, e.CallID)
		c.name, c.state = e.Name, CallRunning
	case *client.ToolProgress:
		if v.landedOuts[e.CallID] {
			return
		}
		c := v.call(e.RunID, e.CallID)
		c.name, c.partial = e.Name, e.Partial
	case *client.ToolFinished:
		v.toolFinished(e)
	case *client.RunEnded:
		v.runEnded(e)
	}
}

func (v *View) item(runID, responseID string, item openresponses.Item, done bool) {
	if item == nil {
		return
	}
	var k key
	switch x := item.(type) {
	case *openresponses.FunctionCallOutput:
		return
	case *openresponses.FunctionCall:
		k = key{call: true, id: x.CallID}
	default:
		k = key{id: client.ItemID(item)}
	}
	if k.id == "" || v.landed(k, responseID) {
		return
	}
	for _, o := range v.items {
		if o.key == k && o.run == runID && o.turn == v.turn.Number && sameTurn(o.responseID, responseID) {
			o.item = item
			if responseID != "" {
				o.responseID = responseID
			}
			o.done = o.done || done
			return
		}
	}
	v.items = append(v.items, &ovItem{run: runID, turn: v.turn.Number, key: k, responseID: responseID, item: item, done: done})
}

func (v *View) call(runID, callID string) *ovCall {
	for _, c := range v.calls {
		if c.callID == callID {
			return c
		}
	}
	c := &ovCall{run: runID, callID: callID}
	v.calls = append(v.calls, c)
	return c
}

func (v *View) toolFinished(e *client.ToolFinished) {
	if v.landedOuts[e.CallID] {
		return
	}
	c := v.call(e.RunID, e.CallID)
	c.name, c.parent, c.finished = e.Name, e.Parent, true
	c.output, c.reason = e.Result, e.Reason
	switch {
	case e.Deferred:
		c.state, c.output = CallDeferred, ""
		v.permit(Permission{CallID: e.CallID, Name: e.Name, Args: c.args, Question: e.Reason, Reason: "deferred"})
	case e.Blocked:
		c.state = CallBlocked
	default:
		c.state = CallEnded
		if e.Err != nil && c.output == "" {
			c.output = e.Err.Error()
		}
	}
}

func (v *View) permit(p Permission) {
	for i := range v.perms {
		if v.perms[i].CallID == p.CallID {
			if p.Question == "" {
				p.Question = v.perms[i].Question
			}
			v.perms[i] = p
			return
		}
	}
	v.perms = append(v.perms, p)
}

func (v *View) runEnded(e *client.RunEnded) {
	// The run's own list of what waits is authoritative: it replaces
	// what the deferred calls reported one by one, keeping their
	// questions.
	old := v.perms
	v.perms, v.cut = nil, nil
	for _, p := range e.Pending {
		next := Permission{CallID: p.CallID, Name: p.Name, Args: p.Args, Reason: string(p.Reason)}
		if e.Reason == agentturn.ReasonInputRequired && p.Reason == agentturn.PendingDeferred {
			for _, o := range old {
				if o.CallID == p.CallID {
					next.Question = o.Question
				}
			}
			v.perms = append(v.perms, next)
		} else {
			v.cut = append(v.cut, next)
		}
	}
	v.turn.RunID, v.turn.Number, v.turn.Attempt, v.turn.Withheld = e.RunID, 0, 0, e.Withheld
	v.turn.State = Idle
	if len(v.perms) > 0 {
		v.turn.State = RequiresAction
	}
	v.liveEnded[e.RunID] = true
	v.flushIfSettled(e.RunID)
}

// Model returns what to render now. It shares nothing with the view.
func (v *View) Model() Model {
	m := Model{
		Session: v.session,
		Leaf:    v.leaf,
		Leaves:  append([]string(nil), v.leaves...),
		Turn:    v.turn,
	}
	committed := map[string]*agentsession.Call{}
	for _, c := range agentsession.Calls(v.path) {
		committed[c.Entry.ID] = c
	}
	var settings agentsession.Settings
	for _, e := range v.path {
		switch x := e.(type) {
		case *agentsession.ConfigEntry:
			settings = settings.Apply(x)
		case *agentsession.ItemEntry:
			if !x.IsVisible() {
				continue
			}
			row := Row{EntryID: x.ID, ResponseID: x.ResponseID, Item: x.Item}
			if fc, ok := x.Item.(*openresponses.FunctionCall); ok {
				row.Call = v.callView(fc, committed[x.ID])
			}
			m.Rows = append(m.Rows, row)
		case *agentsession.CustomEntry:
			// An item the filter keeps from the model is on the record
			// as a custom entry. It is a row all the same: the record
			// holds it, and the overlay row it replaced was shown.
			if item, resp, ok := session.MarkedItem(x); ok {
				m.Rows = append(m.Rows, Row{EntryID: x.ID, ResponseID: resp, Item: item, KeptFromModel: true})
			}
		}
	}
	m.Config = settings.Model
	for _, o := range v.items {
		row := Row{Live: true, Open: !o.done, ResponseID: o.responseID, Item: client.CloneItem(o.item)}
		if fc, ok := o.item.(*openresponses.FunctionCall); ok {
			row.Call = v.callView(fc, nil)
		}
		m.Rows = append(m.Rows, row)
	}
	m.Permissions = append([]Permission(nil), v.perms...)
	m.CutOff = append([]Permission(nil), v.cut...)
	return m
}

// callView merges what the record holds of a call with its overlay.
func (v *View) callView(fc *openresponses.FunctionCall, rec *agentsession.Call) *Call {
	c := &Call{CallID: fc.CallID, Name: fc.Name, Args: fc.Arguments}
	if rec != nil {
		if rec.Dispatch != nil {
			c.State = CallRunning
		}
		if n := len(rec.Decisions); n > 0 {
			d := rec.Decisions[n-1]
			c.Verdict, c.Reason = d.Verdict, d.Reason
		}
		if rec.Output != nil {
			if out, ok := rec.Output.Item.(*openresponses.FunctionCallOutput); ok {
				c.State, c.Output, c.Committed = CallEnded, out.Output.String(), true
				return c
			}
		}
	}
	for _, o := range v.calls {
		switch {
		case o.callID == fc.CallID:
			if o.state > c.State {
				c.State = o.state
			}
			c.Partial, c.Output = o.partial, o.output
			if o.reason != "" {
				c.Reason = o.reason
			}
		case o.parent == fc.CallID:
			c.Children = append(c.Children, Call{CallID: o.callID, Name: o.name, Args: o.args,
				State: o.state, Partial: o.partial, Output: o.output})
		}
	}
	for _, p := range v.perms {
		if p.CallID == fc.CallID && c.State < CallDeferred {
			c.State, c.Reason = CallDeferred, p.Question
		}
	}
	for _, p := range v.cut {
		if p.CallID == fc.CallID && c.State != CallEnded {
			c.State = CallCutOff
		}
	}
	return c
}
