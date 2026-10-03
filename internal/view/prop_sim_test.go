package view

// The generator of the property test: a script is a small program of runs,
// and sim plays it as the agent and its recorder would (agentturn v0.0.16
// and its session package: run.go's stream handling and session.go's
// handle, runStart, turnStart, response, endInFlight), producing one
// reference timeline: the entries in log order, and the live events in
// emission order. What matters to a view is reproduced here and nowhere
// else:
//   - the loop holds a reasoning item's item_end until a message or a call
//     opens (or the response ends), and drops it with a failed attempt;
//   - a message or a call is ended at once, carrying the response ID the
//     stream has named by then, "" before;
//   - the recorder writes an item at its item_end when that carries a
//     response ID, holds one that does not, and writes what it holds, named
//     with the response, as soon as any later item event carries the ID,
//     at response_end, or, for a stream cut off, at the run's end;
//   - a config entry is written by the next entry-writing event, or at the
//     run's start for a recorder that knows the agent's configuration;
//   - a dispatch is written before the tool runs, a run end lists the
//     calls left without an output, and a cut stream's response entry
//     names the response the stream named, if any.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/internal/client"
)

type pItem struct {
	Reuse  int  // 0: a fresh ID; k: the pooled ID k, reused across responses
	Reason bool // a reasoning item rather than a message
	Anon   bool // no ID at all
	Marked bool // a reasoning item the filter keeps from the model
}

type pCall struct {
	Defer    bool // deferred to the caller: the run ends input_required
	Hang     bool // the run is aborted while the tool runs
	Progress bool // the tool reports progress
	Children int  // calls its tool makes
}

type pTurn struct {
	Model     string // moves this turn to another model
	Named     bool   // the stream names its response before its items
	NameAfter int    // else, from this stream item on; 0 never
	Items     []pItem
	Calls     []pCall
	Retries   int  // failed attempts before the one that answers
	Withheld  bool // the last message is withheld by a guard
	Cut       bool // the stream is cut after its completed items
	Failed    bool // the response fails: response_end with a failed response
	Steer     bool // an input is steered in after this turn's tools
}

type pElem struct {
	Kind         string // "run", "rewind", "headrec" or "bookmark"
	Model        string // run: another model from this run on
	Turns        []pTurn
	Approve      bool // a run that answers pending calls approves them
	To           int  // rewind and bookmark: which item or entry
	HiddenPrompt bool // a hidden input follows the prompt
}

type pScript struct {
	Elems []pElem
	// LateCfg is a recorder that does not know the agent's configuration:
	// a config entry waits for the first entry-writing event of a turn.
	LateCfg bool
}

func (s pScript) String() string {
	out := fmt.Sprintf("  lateCfg=%v\n", s.LateCfg)
	for i, e := range s.Elems {
		switch e.Kind {
		case "rewind", "headrec", "bookmark", "link", "compact":
			out += fmt.Sprintf("  %d: %s %d\n", i, e.Kind, e.To)
			continue
		}
		out += fmt.Sprintf("  %d: run model=%q approve=%v hidden=%v\n", i, e.Model, e.Approve, e.HiddenPrompt)
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

// recOp is one thing the record's follower yields in log order: an entry,
// or a head the store records without an entry (a head record, as the cas
// store keeps one).
type recOp struct {
	entry  int    // index into entries
	isHead bool   // a recorded head
	target string // its leaf
}

type timeline struct {
	entries   []agentsession.Entry
	ops       []recOp
	lives     []liveStep
	deferred  map[string]bool   // call IDs ever deferred
	ephemeral map[string]bool   // item uids no entry will ever hold
	entryRun  map[string]string // entry ID to the run that wrote it, "" between runs
	header    agentsession.Header
}

type heldItem struct {
	item   openresponses.Item
	marked bool
}

type sim struct {
	tl       *timeline
	ws       *agentsession.Session
	err      error
	n        int // uid counter
	folds    int
	model    string
	pendCfg  string
	recModel string
	lateCfg  bool
	curRun   string
	// The stream of the turn in flight.
	held         []heldItem // the recorder's: ended before the stream named its response
	loopHeld     []loopReasoning
	inFlight     bool
	respID       string // the ID the stream names, "" while it does not
	respName     string // the response's own ID
	lastHadCalls bool
}

// loopReasoning is a reasoning item the loop has not ended yet.
type loopReasoning struct {
	item   openresponses.Item
	marked bool
	resp   string // the response ID the stream had named when it completed
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
	s.tl.ops = append(s.tl.ops, recOp{entry: len(s.tl.entries)})
	s.tl.entries = append(s.tl.entries, e)
	s.tl.entryRun[id] = s.curRun
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

// entryFor is the entry the recorder writes for an item: an item entry, or,
// for one the filter keeps from the model, the custom entry that names it.
func (s *sim) entryFor(h heldItem, resp string) agentsession.Entry {
	if !h.marked {
		return s.itemEntry(h.item, resp)
	}
	data, err := json.Marshal(h.item)
	if err != nil {
		s.fail(err)
	}
	id, _ := json.Marshal(resp)
	return &agentsession.CustomEntry{NS: h.item.ItemType(), Data: data,
		EntryBase: agentsession.EntryBase{Unknown: map[string]json.RawMessage{session.ResponseIDMember: id}}}
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
	s := &sim{tl: tl, ws: agentsession.New(tl.header), lateCfg: script.LateCfg}
	tl.deferred = map[string]bool{}
	tl.ephemeral = map[string]bool{}
	tl.entryRun = map[string]string{}
	runs := 0
	for _, el := range script.Elems {
		if s.err != nil {
			break
		}
		switch el.Kind {
		case "rewind":
			s.rewind(el.To, false)
		case "headrec":
			s.rewind(el.To, true)
		case "bookmark":
			s.bookmark(el.To)
		case "link":
			s.link(el.To)
		case "compact":
			s.compact(el.To)
		default:
			runs++
			s.run(el, runs)
		}
	}
	return s.err
}

func (s *sim) rewind(to int, record bool) {
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
	if record {
		s.tl.ops = append(s.tl.ops, recOp{isHead: true, target: target})
		return
	}
	mark, err := s.ws.MarkLeaf()
	if err != nil {
		s.fail(err)
		return
	}
	s.write(mark)
}

// bookmark labels an entry of the line without moving the head.
func (s *sim) bookmark(to int) {
	path := s.ws.Path(s.ws.Leaf())
	if len(path) == 0 {
		s.fail(fmt.Errorf("bookmark: empty line"))
		return
	}
	s.write(agentsession.NewLabelEntry(path[to%len(path)].Base().ID, "bookmark"))
}

// link records a subsession at the leaf.
func (s *sim) link(to int) {
	s.write(agentsession.NewSubsessionLink(fmt.Sprintf("child_%d", to), fmt.Sprintf("call_link_%d", to)))
}

// compact folds the line before one of its items into a summary.
func (s *sim) compact(to int) {
	var items []string
	for _, e := range s.ws.Path(s.ws.Leaf()) {
		if it, ok := e.(*agentsession.ItemEntry); ok && it.IsVisible() {
			items = append(items, it.ID)
		}
	}
	if len(items) == 0 {
		return
	}
	s.folds++
	summary := openresponses.UserText(fmt.Sprintf("fold_%d the summary", s.folds))
	summary.Role = openresponses.RoleAssistant
	c := &agentsession.CompactionEntry{FirstKept: items[to%len(items)], Summary: summary}
	if to%3 == 0 {
		c.Pinned = openresponses.Items{openresponses.UserText(fmt.Sprintf("pin_%d", s.folds))}
	}
	s.write(c)
}

func (s *sim) run(el pElem, n int) {
	runID := fmt.Sprintf("run_%d", n)
	pend := s.pending()
	resume := len(pend) > 0
	src := agentsession.SourceInput
	if resume {
		src = agentsession.SourceResume
	}
	s.curRun = runID
	s.write(agentsession.NewRunStart(runID, src, ""))
	s.live(&client.RunStarted{RunID: runID, Resume: resume})
	if el.Model != "" {
		s.model = el.Model
	}
	// A recorder that knows the configuration settles it at the run's
	// start; one that does not, at the turn's first writing event.
	if !s.lateCfg && s.model != "" && s.model != s.recModel {
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
	if el.HiddenPrompt {
		// An item the caller marked hidden: in the context, written with
		// visible false, and the stream does not carry it.
		e := s.itemEntry(openresponses.UserText(s.uid("h")), "")
		no := false
		e.Visible = &no
		s.write(e)
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
	s.curRun = ""
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
		// A stream cut off before its response ended: what the recorder
		// holds is written as the stream left it, naming the response
		// only if the stream did, then the failed response, which names
		// the same.
		s.flushHeld(s.respID)
		s.write(&agentsession.ResponseEntry{ResponseID: s.respID, Status: openresponses.ResponseStatusFailed})
		s.inFlight = false
	}
	s.write(agentsession.NewRunEnd(runID, reason, "", ids))
	s.live(&client.RunEnded{RunID: runID, Reason: live, Withheld: withheld, Pending: lp})
}

func textItem(item openresponses.Item, text string) openresponses.Item {
	switch v := client.CloneItem(item).(type) {
	case *openresponses.Message:
		return &openresponses.Message{ID: v.ID, Role: v.Role, Content: openresponses.Contents{&openresponses.OutputText{Text: text}}}
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

func partialOf(item openresponses.Item) openresponses.Item {
	if _, isCall := item.(*openresponses.FunctionCall); isCall {
		return item
	}
	return textItem(item, strings.TrimSuffix(itemText(item), "ne"))
}

// flushHeld writes the items the recorder holds, named with resp.
func (s *sim) flushHeld(resp string) {
	for _, h := range s.held {
		s.write(s.entryFor(h, resp))
	}
	s.held = nil
}

// endItem delivers an item_end: the recorder writes the item when the event
// carries a response ID, after naming what it holds, and holds it when it
// does not.
func (s *sim) endItem(runID string, h heldItem, resp string) {
	s.flush()
	if resp != "" {
		s.flushHeld(resp)
		s.write(s.entryFor(h, resp))
	} else {
		s.held = append(s.held, h)
	}
	s.live(&client.ItemCompleted{RunID: runID, ResponseID: resp, Item: client.CloneItem(h.item)})
}

// commit ends the reasoning items the loop held, as a message or a call
// opens, or the response ends.
func (s *sim) commit(runID string) {
	held := s.loopHeld
	s.loopHeld = nil
	for _, r := range held {
		s.endItem(runID, heldItem{item: r.item, marked: r.marked}, r.resp)
	}
}

// openItem plays an item's item_start and one item_update, with the
// response ID the stream carries by now.
func (s *sim) openItem(runID string, item openresponses.Item) {
	p := partialOf(item)
	s.live(&client.ItemOpened{RunID: runID, ResponseID: s.respID, Item: client.CloneItem(p)})
	s.live(&client.ItemUpdated{RunID: runID, ResponseID: s.respID, Item: client.CloneItem(p)})
}

type callRec struct {
	id   string
	spec pCall
}

func (s *sim) turn(runID string, n int, t pTurn) endKind {
	s.lastHadCalls = false
	if t.Withheld {
		t.Calls = nil
	}
	if t.Model != "" {
		s.model = t.Model
	}
	if s.model != "" && s.model != s.recModel && (s.lateCfg || t.Model != "") {
		s.pendCfg = s.model
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
	s.held, s.loopHeld = nil, nil
	used := map[string]bool{}

	// The stream's items in the order a model makes them: reasoning, then
	// messages, then calls.
	type streamItem struct {
		item   openresponses.Item
		marked bool
		call   *callRec
	}
	var items []streamItem
	for _, it := range t.Items {
		if it.Reason {
			items = append(items, streamItem{item: s.mkItem(it, used, s.uid("m")+" done"), marked: it.Marked})
		}
	}
	for _, it := range t.Items {
		if !it.Reason {
			items = append(items, streamItem{item: s.mkItem(it, used, s.uid("m")+" done")})
		}
	}
	var calls []*callRec
	for _, c := range t.Calls {
		callID := s.uid("call")
		fc := &openresponses.FunctionCall{ID: "fc_" + callID, CallID: callID, Name: "tool", Arguments: "{}"}
		rec := &callRec{id: callID, spec: c}
		calls = append(calls, rec)
		items = append(items, streamItem{item: fc, call: rec})
		if c.Defer {
			s.tl.deferred[callID] = true
		}
	}
	// Whatever never completes has no entry coming.
	dropLoopHeld := func() {
		for _, r := range s.loopHeld {
			s.tl.ephemeral[firstToken(itemText(r.item))] = true
		}
		s.loopHeld = nil
	}
	for i, si := range items {
		if !t.Named && t.NameAfter > 0 && i >= t.NameAfter && s.respID == "" {
			// The stream names its response from here: the recorder
			// names what it holds before this event.
			s.respID = s.respName
			s.flushHeld(s.respID)
		}
		_, reasoning := si.item.(*openresponses.ReasoningItem)
		if reasoning {
			s.openItem(runID, si.item)
			s.loopHeld = append(s.loopHeld, loopReasoning{item: si.item, marked: si.marked, resp: s.respID})
			continue
		}
		// A message or a call opening commits the attempt: the reasoning
		// the loop held is ended first.
		s.commit(runID)
		s.openItem(runID, si.item)
		s.endItem(runID, heldItem{item: si.item}, s.respID)
	}
	if t.Cut || t.Withheld || t.Failed {
		x := s.uid("x")
		s.tl.ephemeral[x] = true
		extra := s.mkItem(pItem{}, used, x+" done")
		s.openItem(runID, extra)
	}
	if t.Cut {
		dropLoopHeld()
		if n%2 == 0 {
			return endFail
		}
		return endAbort
	}
	if t.Withheld || t.Failed {
		dropLoopHeld()
	} else {
		s.commit(runID)
	}
	s.flush()
	s.flushHeld(s.respName) // the response's end names it, whatever the stream did
	status := openresponses.ResponseStatusCompleted
	if t.Withheld {
		status = openresponses.ResponseStatusIncomplete
	}
	if t.Failed {
		status = openresponses.ResponseStatusFailed
	}
	s.write(&agentsession.ResponseEntry{ResponseID: s.respName, Status: status})
	s.inFlight = false
	s.live(&client.ResponseCompleted{RunID: runID, ResponseID: s.respName, Withheld: t.Withheld})
	if t.Withheld {
		return endWithheld
	}
	if t.Failed {
		return endFail
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
		default:
			s.write(agentsession.NewDispatch(c.id, call))
			s.live(&client.ToolDispatched{RunID: runID, CallID: c.id, Name: "tool"})
			if c.spec.Progress {
				s.live(&client.ToolProgress{RunID: runID, CallID: c.id, Name: "tool", Partial: "p-" + c.id})
			}
			for k := 0; k < c.spec.Children; k++ {
				child := fmt.Sprintf("%s.%d", c.id, k)
				s.live(&client.ToolOpened{RunID: runID, CallID: child, Name: "child", Args: "{}", Parent: c.id})
				s.write(&agentsession.CustomEntry{NS: "agentturn.nested", Data: json.RawMessage(`{}`), CallID: c.id})
				s.live(&client.ToolFinished{RunID: runID, CallID: child, Name: "child", Result: "C-" + child, Parent: c.id})
			}
			if c.spec.Hang {
				s.live(&client.ToolFinished{RunID: runID, CallID: c.id, Name: "tool", Err: errors.New("aborted")})
				end = endAbort
			} else {
				s.live(&client.ToolFinished{RunID: runID, CallID: c.id, Name: "tool", Result: "R-" + c.id})
				done = append(done, c.id)
			}
		}
		if end == endAbort {
			break
		}
	}
	for _, id := range done {
		s.output(runID, id, "R-"+id)
	}
	s.lastHadCalls = len(calls) > 0 && end == endNone
	if t.Steer && s.lastHadCalls {
		// An input steered in while the tools ran: the recorder writes
		// the queued entry when it is accepted, and the loop appends the
		// message before the next model call.
		item := openresponses.UserText(s.uid("u"))
		q := s.write(agentsession.NewQueued(client.CloneItem(item), agentsession.ModeSteer))
		s.live(&client.ItemOpened{RunID: runID, Item: client.CloneItem(item)})
		e := s.itemEntry(item, "")
		e.QueuedFrom = q
		s.write(e)
		s.live(&client.ItemCompleted{RunID: runID, Item: client.CloneItem(item)})
	}
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
