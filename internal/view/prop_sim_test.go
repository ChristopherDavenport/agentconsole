package view

// The generator of the property test: a script is a small program of runs,
// and sim plays it as the agent and its recorder would, producing one
// reference timeline: the entries in log order, and the live events in
// emission order. The recorder's behaviour that matters to a view is
// reproduced here, and nowhere else: an item with a named response is
// written at its item_end; one whose stream has not named the response is
// held until response_end; a config entry is written by the next
// entry-writing event; a dispatch is written before the tool runs; a run
// end lists the calls left without an output.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/internal/client"
)

type pItem struct {
	Reuse  int  // 0: a fresh ID; k: the pooled ID k, reused across responses
	Reason bool // a reasoning item rather than a message
	Anon   bool // no ID at all
}

type pCall struct {
	Defer bool // deferred to the caller: the run ends input_required
	Hang  bool // the run is aborted while the tool runs
}

type pTurn struct {
	Model    string // moves this turn to another model
	Named    bool   // the stream names its response before its items
	Items    []pItem
	Calls    []pCall
	Retries  int  // failed attempts before the one that answers
	Withheld bool // the last message is withheld by a guard
	Cut      bool // the stream is cut after its completed items
}

type pElem struct {
	Kind    string // "run" or "rewind"
	Model   string // run: another model from this run on
	Turns   []pTurn
	Approve bool // a run that answers pending calls approves them
	To      int  // rewind: which item of the line to move the head to
}

type pScript struct{ Elems []pElem }

func (s pScript) String() string {
	out := ""
	for i, e := range s.Elems {
		if e.Kind == "rewind" {
			out += fmt.Sprintf("  %d: rewind to item %d\n", i, e.To)
			continue
		}
		out += fmt.Sprintf("  %d: run model=%q approve=%v\n", i, e.Model, e.Approve)
		for j, t := range e.Turns {
			out += fmt.Sprintf("       turn %d: %+v\n", j, t)
		}
	}
	return out
}

type liveStep struct {
	ev    client.LiveEvent
	after int // entries written before the event was queued
}

type timeline struct {
	entries   []agentsession.Entry
	lives     []liveStep
	deferred  map[string]bool // call IDs ever deferred
	ephemeral map[string]bool // item uids no entry will ever hold
	header    agentsession.Header
}

type sim struct {
	tl       *timeline
	ws       *agentsession.Session
	err      error
	n        int // uid counter
	model    string
	pendCfg  string
	recModel string
	// The stream of the turn in flight.
	held         []openresponses.Item // completed before the stream named its response
	inFlight     bool
	respID       string // the ID the stream names, "" while it does not
	respName     string // the response's own ID
	lastHadCalls bool
}

func (s *sim) fail(err error) {
	if s.err == nil {
		s.err = err
	}
}

func (s *sim) uid(prefix string) string {
	s.n++
	return fmt.Sprintf("%s%d", prefix, s.n)
}

func (s *sim) write(e agentsession.Entry) string {
	if s.err != nil {
		return ""
	}
	id, err := s.ws.Append(e)
	if err != nil {
		s.fail(fmt.Errorf("append %s: %w", e.EntryType(), err))
		return ""
	}
	s.tl.entries = append(s.tl.entries, e)
	return id
}

func (s *sim) live(ev client.LiveEvent) {
	s.tl.lives = append(s.tl.lives, liveStep{ev: ev, after: len(s.tl.entries)})
}

// flush writes the config entry waiting for an entry-writing event.
func (s *sim) flush() {
	if s.pendCfg != "" && s.pendCfg != s.recModel {
		s.write(&agentsession.ConfigEntry{Model: s.pendCfg})
		s.recModel = s.pendCfg
	}
	s.pendCfg = ""
}

func (s *sim) itemEntry(item openresponses.Item, resp string) *agentsession.ItemEntry {
	e := agentsession.NewItemEntry(item)
	e.ResponseID = resp
	return e
}

func (s *sim) pending() []*agentsession.Call {
	if s.ws.Leaf() == "" {
		return nil
	}
	calls, err := s.ws.PendingCalls(s.ws.Leaf())
	if err != nil {
		s.fail(err)
	}
	return calls
}

func (s *sim) mkItem(it pItem, used map[string]bool, text string) openresponses.Item {
	id := ""
	if !it.Anon {
		prefix := "msg"
		if it.Reason {
			prefix = "rs"
		}
		if it.Reuse > 0 {
			id = fmt.Sprintf("%s_r%d", prefix, it.Reuse)
		}
		if id == "" || used[id] {
			id = fmt.Sprintf("%s_f%d", prefix, s.n+1000)
		}
		used[id] = true
	}
	if it.Reason {
		return &openresponses.ReasoningItem{ID: id, Summary: openresponses.Contents{&openresponses.SummaryText{Text: text}}}
	}
	m := openresponses.AssistantText(text)
	m.ID = id
	return m
}

type endKind int

const (
	endNone endKind = iota
	endInput
	endAbort
	endFail
	endWithheld
)

// play runs the script.
func (tl *timeline) play(script pScript) error {
	s := &sim{tl: tl, ws: agentsession.New(tl.header)}
	tl.deferred = map[string]bool{}
	tl.ephemeral = map[string]bool{}
	runs := 0
	for _, el := range script.Elems {
		if s.err != nil {
			break
		}
		switch el.Kind {
		case "rewind":
			s.rewind(el.To)
		default:
			runs++
			s.run(el, runs)
		}
	}
	return s.err
}

func (s *sim) rewind(to int) {
	// The head moves back to an item of the line: the writer branches
	// there and records it with a leaf label.
	var items []string
	for _, e := range s.ws.Path(s.ws.Leaf()) {
		if it, ok := e.(*agentsession.ItemEntry); ok && it.IsVisible() {
			items = append(items, it.ID)
		}
	}
	if len(items) == 0 {
		s.fail(fmt.Errorf("rewind: no items"))
		return
	}
	target := items[to%len(items)]
	if err := s.ws.Branch(target); err != nil {
		s.fail(err)
		return
	}
	mark, err := s.ws.MarkLeaf()
	if err != nil {
		s.fail(err)
		return
	}
	s.write(mark)
}

func (s *sim) run(el pElem, n int) {
	runID := fmt.Sprintf("run_%d", n)
	pend := s.pending()
	resume := len(pend) > 0
	src := agentsession.SourceInput
	if resume {
		src = agentsession.SourceResume
	}
	s.write(agentsession.NewRunStart(runID, src, ""))
	s.live(&client.RunStarted{RunID: runID, Resume: resume})
	if el.Model != "" {
		s.model = el.Model
	}
	if s.model != "" && s.model != s.recModel {
		s.pendCfg = s.model
		s.flush()
	}
	if resume {
		s.answer(runID, pend, el.Approve)
	} else {
		text := s.uid("u")
		item := openresponses.UserText(text)
		s.live(&client.ItemOpened{RunID: runID, Item: client.CloneItem(item)})
		s.write(s.itemEntry(item, ""))
		s.live(&client.ItemCompleted{RunID: runID, Item: client.CloneItem(item)})
	}
	turns := el.Turns
	if len(turns) == 0 {
		turns = []pTurn{{Named: true, Items: []pItem{{}}}}
	}
	s.lastHadCalls = resume // the model is called on the outputs
	end := endNone
	for i := 0; i < len(turns)+1 && end == endNone; i++ {
		var t pTurn
		if i < len(turns) {
			t = turns[i]
		} else if s.lastHadCalls {
			t = pTurn{Named: true, Items: []pItem{{}}} // the model answers the outputs
		} else {
			break
		}
		end = s.turn(runID, i+1, t)
		if !s.lastHadCalls && end == endNone {
			break // the model answered with no call: the run is done
		}
	}
	s.finish(runID, end)
}

func (s *sim) answer(runID string, pend []*agentsession.Call, approve bool) {
	for _, c := range pend {
		callID := c.ID()
		held := c.Held()
		dispatched := c.Dispatch != nil
		switch {
		case held && approve:
			s.write(agentsession.NewDecision(callID, c.Entry.ID, agentsession.VerdictProceed, "user"))
			s.live(&client.ToolOpened{RunID: runID, CallID: callID, Name: c.Call.Name, Args: c.Call.Arguments})
			s.flush()
			s.write(agentsession.NewDispatch(callID, c.Entry.ID))
			s.live(&client.ToolDispatched{RunID: runID, CallID: callID, Name: c.Call.Name})
			s.live(&client.ToolFinished{RunID: runID, CallID: callID, Name: c.Call.Name, Result: "ran"})
			s.output(runID, callID, "ran")
		case dispatched && !held:
			s.write(agentsession.NewDecision(callID, c.Entry.ID, agentsession.VerdictAnswer, "user").WithReason("unknown"))
			s.output(runID, callID, "answered")
		default:
			s.write(agentsession.NewDecision(callID, c.Entry.ID, agentsession.VerdictReject, "user").WithReason("refused"))
			s.output(runID, callID, "refused")
		}
	}
}

func (s *sim) output(runID, callID, text string) {
	out := openresponses.NewFunctionCallOutput(callID, text)
	s.live(&client.ItemOpened{RunID: runID, Item: client.CloneItem(out)})
	s.flush()
	s.write(s.itemEntry(out, ""))
	s.live(&client.ItemCompleted{RunID: runID, Item: client.CloneItem(out)})
}

func (s *sim) finish(runID string, end endKind) {
	pend := s.pending()
	var ids []string
	var lp []client.Pending
	for _, c := range pend {
		ids = append(ids, c.ID())
		reason := agentturn.PendingAborted
		if s.tl.deferred[c.ID()] && c.Held() {
			reason = agentturn.PendingDeferred
		}
		lp = append(lp, client.Pending{CallID: c.ID(), Name: c.Call.Name, Args: c.Call.Arguments, Reason: reason})
	}
	reason, live := agentsession.ReasonDone, agentturn.ReasonDone
	withheld := false
	switch end {
	case endInput:
		reason, live = agentsession.ReasonInputRequired, agentturn.ReasonInputRequired
	case endAbort:
		reason, live = agentsession.ReasonInterrupted, agentturn.ReasonAborted
	case endFail:
		reason, live = agentsession.ReasonError, agentturn.ReasonError
	case endWithheld:
		reason, live, withheld = agentsession.ReasonAborted, agentturn.ReasonStopped, true
	}
	s.flush()
	if s.inFlight {
		// A stream cut off: what it completed before the cut is written,
		// then the failed response.
		s.flushHeld(s.respID)
		s.write(&agentsession.ResponseEntry{ResponseID: s.respName, Status: openresponses.ResponseStatusFailed})
		s.inFlight = false
	}
	s.write(agentsession.NewRunEnd(runID, reason, "", ids))
	s.live(&client.RunEnded{RunID: runID, Reason: live, Withheld: withheld, Pending: lp})
}

func textItem(item openresponses.Item, text string) openresponses.Item {
	switch v := client.CloneItem(item).(type) {
	case *openresponses.Message:
		return openresponses.Items{&openresponses.Message{ID: v.ID, Role: v.Role, Content: openresponses.Contents{&openresponses.OutputText{Text: text}}}}[0]
	case *openresponses.ReasoningItem:
		v.Summary = openresponses.Contents{&openresponses.SummaryText{Text: text}}
		return v
	case *openresponses.FunctionCall:
		return v
	}
	return item
}

func itemText(item openresponses.Item) string {
	switch v := item.(type) {
	case *openresponses.Message:
		return v.Text()
	case *openresponses.ReasoningItem:
		return v.Summary.Text()
	}
	return ""
}

// stream plays one model item: opened, updated and, unless it is cut off,
// completed, with its entry written at its end when the stream has named
// its response and held until the response's end when it has not. It
// returns the ID of the entry, "" while the item is held or cut off.
func (s *sim) stream(runID string, item openresponses.Item, complete bool) string {
	partial := item
	if _, isCall := item.(*openresponses.FunctionCall); !isCall {
		partial = textItem(item, strings.TrimSuffix(itemText(item), "ne"))
	}
	s.live(&client.ItemOpened{RunID: runID, ResponseID: s.respID, Item: client.CloneItem(partial)})
	s.live(&client.ItemUpdated{RunID: runID, ResponseID: s.respID, Item: client.CloneItem(partial)})
	if !complete {
		return ""
	}
	if s.respID != "" {
		s.flush()
		id := s.write(s.itemEntry(item, s.respID))
		s.live(&client.ItemCompleted{RunID: runID, ResponseID: s.respID, Item: client.CloneItem(item)})
		return id
	}
	s.live(&client.ItemCompleted{RunID: runID, Item: client.CloneItem(item)})
	s.held = append(s.held, item)
	return ""
}

// flushHeld writes the items held for the response.
func (s *sim) flushHeld(resp string) {
	for _, it := range s.held {
		s.write(s.itemEntry(it, resp))
	}
	s.held = nil
}

type callRec struct {
	id    string
	spec  pCall
	entry string
}

func (s *sim) turn(runID string, n int, t pTurn) endKind {
	s.lastHadCalls = false
	if t.Withheld {
		t.Calls = nil
	}
	if t.Model != "" {
		s.model = t.Model
		if s.model != s.recModel {
			s.pendCfg = s.model
		}
	}
	s.live(&client.TurnStarted{RunID: runID, Turn: n, Model: s.model})
	s.respName = s.uid("resp")
	s.respID = ""
	if t.Named {
		s.respID = s.respName
	}
	for a := 0; a < t.Retries; a++ {
		x := s.uid("x")
		s.tl.ephemeral[x] = true
		item := textItem(openresponses.AssistantText(""), x+" do")
		item.(*openresponses.Message).ID = s.uid("msg_failed")
		s.live(&client.ItemOpened{RunID: runID, ResponseID: s.respID, Item: client.CloneItem(item)})
		s.live(&client.ItemUpdated{RunID: runID, ResponseID: s.respID, Item: client.CloneItem(item)})
		s.live(&client.ModelRetrying{RunID: runID, Turn: n, Attempt: a + 1})
	}
	s.inFlight = true
	s.held = nil
	used := map[string]bool{}
	for _, it := range t.Items {
		item := s.mkItem(it, used, s.uid("m")+" done")
		s.stream(runID, item, true)
	}
	var calls []callRec
	for _, c := range t.Calls {
		callID := s.uid("call")
		fc := &openresponses.FunctionCall{ID: "fc_" + callID, CallID: callID, Name: "tool", Arguments: "{}"}
		id := s.stream(runID, fc, true)
		calls = append(calls, callRec{id: id, spec: c})
		if c.Defer {
			s.tl.deferred[callID] = true
		}
		calls[len(calls)-1].id = callID
	}
	if t.Cut || t.Withheld {
		x := s.uid("x")
		s.tl.ephemeral[x] = true
		extra := s.mkItem(pItem{}, used, x+" done")
		s.stream(runID, extra, false)
	}
	if t.Cut {
		if n%2 == 0 {
			return endFail
		}
		return endAbort
	}
	s.flush()
	s.flushHeld(s.respName) // the response's end names it, whatever the stream did
	status := openresponses.ResponseStatusCompleted
	if t.Withheld {
		status = openresponses.ResponseStatusIncomplete
	}
	s.write(&agentsession.ResponseEntry{ResponseID: s.respName, Status: status})
	s.inFlight = false
	s.live(&client.ResponseCompleted{RunID: runID, ResponseID: s.respName, Withheld: t.Withheld})
	if t.Withheld {
		return endWithheld
	}
	end := endNone
	var done []string
	for _, c := range calls {
		call := s.callEntryOf(c.id)
		s.live(&client.ToolOpened{RunID: runID, CallID: c.id, Name: "tool", Args: "{}"})
		s.flush()
		switch {
		case c.spec.Defer:
			s.write(agentsession.NewDecision(c.id, call, agentsession.VerdictHold, "policy").WithReason("q-" + c.id))
			s.live(&client.ToolFinished{RunID: runID, CallID: c.id, Name: "tool", Deferred: true, Reason: "q-" + c.id})
			if end == endNone {
				end = endInput
			}
		case c.spec.Hang:
			s.write(agentsession.NewDispatch(c.id, call))
			s.live(&client.ToolDispatched{RunID: runID, CallID: c.id, Name: "tool"})
			s.live(&client.ToolFinished{RunID: runID, CallID: c.id, Name: "tool", Err: errors.New("aborted")})
			end = endAbort
		default:
			s.write(agentsession.NewDispatch(c.id, call))
			s.live(&client.ToolDispatched{RunID: runID, CallID: c.id, Name: "tool"})
			s.live(&client.ToolFinished{RunID: runID, CallID: c.id, Name: "tool", Result: "R-" + c.id})
			done = append(done, c.id)
		}
		if end == endAbort {
			break
		}
	}
	for _, id := range done {
		s.output(runID, id, "R-"+id)
	}
	s.lastHadCalls = len(calls) > 0 && end == endNone
	return end
}

// callEntryOf finds the entry that holds a call.
func (s *sim) callEntryOf(callID string) string {
	for _, c := range agentsession.Calls(s.ws.Path(s.ws.Leaf())) {
		if c.ID() == callID {
			return c.Entry.ID
		}
	}
	s.fail(fmt.Errorf("call %s not on the path", callID))
	return ""
}
