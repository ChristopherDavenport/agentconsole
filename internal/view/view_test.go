package view

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/internal/client"
)

// log is a session the tests write by hand, handing the view the same
// changes a follower would.
type log struct {
	t *testing.T
	s *agentsession.Session
	v *View
}

func newLog(t *testing.T) *log {
	t.Helper()
	l := &log{t: t, s: agentsession.New(agentsession.Header{}), v: New()}
	l.v.Record(agentsession.Change{Kind: agentsession.Snapshot, Session: l.s})
	return l
}

func (l *log) append(e agentsession.Entry) string {
	l.t.Helper()
	id, err := l.s.Append(e)
	if err != nil {
		l.t.Fatal(err)
	}
	l.v.Record(agentsession.Change{Kind: agentsession.Appended, Session: l.s, ID: id, Entry: e})
	return id
}

func msg(id, role, text string) *openresponses.Message {
	m := openresponses.UserText(text)
	m.ID = id
	if role == "assistant" {
		m.Role = openresponses.RoleAssistant
	}
	return m
}

func itemEntry(item openresponses.Item, resp string) *agentsession.ItemEntry {
	e := agentsession.NewItemEntry(item)
	e.ResponseID = resp
	return e
}

func (l *log) stream(run, resp string, item openresponses.Item, done bool) {
	if done {
		l.v.Live(&client.ItemCompleted{RunID: run, ResponseID: resp, Item: item})
		return
	}
	l.v.Live(&client.ItemUpdated{RunID: run, ResponseID: resp, Item: item})
}

func liveRows(m Model) (n int) {
	for _, r := range m.Rows {
		if r.Live {
			n++
		}
	}
	return n
}

func TestOverlayIsDroppedWhenItsEntryLands(t *testing.T) {
	l := newLog(t)
	l.v.Live(&client.RunStarted{RunID: "run1"})
	l.stream("run1", "resp1", msg("m1", "assistant", "hel"), false)
	if m := l.v.Model(); len(m.Rows) != 1 || !m.Rows[0].Live || !m.Rows[0].Open {
		t.Fatalf("streaming: %+v", m.Rows)
	}
	l.stream("run1", "resp1", msg("m1", "assistant", "hello"), true)
	if m := l.v.Model(); len(m.Rows) != 1 || m.Rows[0].Open {
		t.Fatalf("completed but not committed must stay as a closed overlay row: %+v", m.Rows)
	}
	l.append(itemEntry(msg("m1", "assistant", "hello"), "resp1"))
	m := l.v.Model()
	if len(m.Rows) != 1 || m.Rows[0].Live || m.Rows[0].EntryID == "" {
		t.Fatalf("after the entry: %+v", m.Rows)
	}
}

func TestEntryBeforeTheLiveEventLeavesNoGhost(t *testing.T) {
	l := newLog(t)
	l.v.Live(&client.RunStarted{RunID: "run1"})
	l.stream("run1", "resp1", msg("m1", "assistant", "hel"), false)
	l.append(itemEntry(msg("m1", "assistant", "hello"), "resp1"))
	// The follower beat the live consumer: what the stream still has to
	// say about the item is stale.
	l.stream("run1", "resp1", msg("m1", "assistant", "hello"), true)
	l.v.Live(&client.ItemOpened{RunID: "run1", ResponseID: "resp1", Item: msg("m1", "assistant", "hello")})
	if m := l.v.Model(); len(m.Rows) != 1 || liveRows(m) != 0 {
		t.Fatalf("rows = %+v, want the committed one alone", m.Rows)
	}
}

func TestUnnamedItemKeepsItsOverlayUntilResponseEnd(t *testing.T) {
	l := newLog(t)
	l.v.Live(&client.RunStarted{RunID: "run1"})
	l.stream("run1", "", msg("m1", "assistant", "hi"), true)
	l.v.Live(&client.ResponseCompleted{RunID: "run1", ResponseID: "resp1"})
	m := l.v.Model()
	if len(m.Rows) != 1 || !m.Rows[0].Live || m.Rows[0].ResponseID != "resp1" {
		t.Fatalf("after response_end, before the entry: %+v", m.Rows)
	}
	l.append(itemEntry(msg("m1", "assistant", "hi"), "resp1"))
	if m := l.v.Model(); len(m.Rows) != 1 || m.Rows[0].Live {
		t.Fatalf("after the entry: %+v", m.Rows)
	}
}

func TestRunEndFlushesOnlyWhenTheRecordHasTheEndToo(t *testing.T) {
	for _, entryFirst := range []bool{false, true} {
		name := "live end first"
		if entryFirst {
			name = "entry end first"
		}
		t.Run(name, func(t *testing.T) {
			l := newLog(t)
			l.v.Live(&client.RunStarted{RunID: "run1"})
			l.append(agentsession.NewRunStart("run1", agentsession.SourceInput, ""))
			l.stream("run1", "resp1", msg("m1", "assistant", "orphan"), true)
			end := func() { l.append(agentsession.NewRunEnd("run1", "done", "", nil)) }
			live := func() { l.v.Live(&client.RunEnded{RunID: "run1", Reason: agentturn.ReasonDone}) }
			if entryFirst {
				end()
				if liveRows(l.v.Model()) != 1 {
					t.Fatal("flushed on the record's end alone")
				}
				live()
			} else {
				live()
				if liveRows(l.v.Model()) != 1 {
					t.Fatal("flushed on the live end alone, before the entries could land")
				}
				end()
			}
			m := l.v.Model()
			if liveRows(m) != 0 || m.Turn.State != Idle {
				t.Fatalf("after both ends: %+v, turn %+v", m.Rows, m.Turn)
			}
		})
	}
}

// A run that ended and whose entries the follower has not delivered yet
// keeps its overlay when the next run starts: the rows are not dropped
// before their entries exist.
func TestNextRunDoesNotDropWhatTheRecordHasNotDelivered(t *testing.T) {
	l := newLog(t)
	l.v.Live(&client.RunStarted{RunID: "run1"})
	l.stream("run1", "resp1", msg("m1", "assistant", "pending entry"), true)
	l.v.Live(&client.RunEnded{RunID: "run1", Reason: agentturn.ReasonDone})
	l.v.Live(&client.RunStarted{RunID: "run2"})
	if m := l.v.Model(); len(m.Rows) != 1 {
		t.Fatalf("run1's row was dropped before its entry landed: %+v", m.Rows)
	}
	l.append(agentsession.NewRunStart("run1", agentsession.SourceInput, ""))
	l.append(itemEntry(msg("m1", "assistant", "pending entry"), "resp1"))
	l.append(agentsession.NewRunEnd("run1", "done", "", nil))
	m := l.v.Model()
	if len(m.Rows) != 1 || m.Rows[0].Live {
		t.Fatalf("after run1's entries: %+v", m.Rows)
	}
}

// Settling a run leaves no state behind.
func TestSettledRunLeavesNoState(t *testing.T) {
	l := newLog(t)
	l.v.Live(&client.RunStarted{RunID: "run1"})
	l.v.Live(&client.RunEnded{RunID: "run1", Reason: agentturn.ReasonDone})
	l.append(agentsession.NewRunEnd("run1", "done", "", nil))
	if len(l.v.liveEnded) != 0 || len(l.v.endSeen) != 0 {
		t.Errorf("settled run left state: %d live ends, %d entry ends", len(l.v.liveEnded), len(l.v.endSeen))
	}
}

// A run that never wrote its end entry is settled by the start entry of
// the run after it, which lands behind everything the first wrote.
func TestNextRunStartEntrySettlesARunWithNoEndEntry(t *testing.T) {
	l := newLog(t)
	l.v.Live(&client.RunStarted{RunID: "run1"})
	l.stream("run1", "resp1", msg("m1", "assistant", "lost"), true)
	l.v.Live(&client.RunEnded{RunID: "run1", Reason: agentturn.ReasonError})
	l.v.Live(&client.RunStarted{RunID: "run2"})
	l.append(agentsession.NewRunStart("run2", agentsession.SourceInput, ""))
	if m := l.v.Model(); liveRows(m) != 0 {
		t.Fatalf("the orphan outlived the next run's start entry: %+v", m.Rows)
	}
}

// The state kept for run ends is bounded: a long history adds none, and
// a session written by another process, which no live event ever
// settles, adds a capped number.
func TestRunEndStateIsBounded(t *testing.T) {
	l := newLog(t)
	for i := range 300 {
		id := fmt.Sprintf("run%d", i)
		l.append(agentsession.NewRunStart(id, agentsession.SourceInput, ""))
		l.append(agentsession.NewRunEnd(id, "done", "", nil))
	}
	if len(l.v.endSeen) > maxEndSeen {
		t.Errorf("appended run ends kept: %d", len(l.v.endSeen))
	}
	l.v.Record(agentsession.Change{Kind: agentsession.Reset, Session: l.s})
	if len(l.v.endSeen) > maxEndSeen {
		t.Errorf("rebuild kept %d run ends", len(l.v.endSeen))
	}
}

func TestAnonymousAndOutputItemsAreNotOverlaid(t *testing.T) {
	l := newLog(t)
	l.v.Live(&client.RunStarted{RunID: "run1"})
	l.stream("run1", "", openresponses.UserText("no id"), true)
	l.stream("run1", "", openresponses.NewFunctionCallOutput("c1", "out"), true)
	if m := l.v.Model(); len(m.Rows) != 0 {
		t.Fatalf("rows = %+v, want none", m.Rows)
	}
}

func TestCallLifecycleAndKeys(t *testing.T) {
	l := newLog(t)
	call := &openresponses.FunctionCall{ID: "fc1", CallID: "c1", Name: "upper", Arguments: `{"text":"a"}`}
	l.v.Live(&client.RunStarted{RunID: "run1"})
	l.stream("run1", "resp1", call, true)
	l.v.Live(&client.ToolOpened{RunID: "run1", CallID: "c1", Name: "upper", Args: call.Arguments})
	if m := l.v.Model(); m.Rows[0].Call == nil || m.Rows[0].Call.State != CallOpen {
		t.Fatalf("opened: %+v", m.Rows[0].Call)
	}
	l.v.Live(&client.ToolDispatched{RunID: "run1", CallID: "c1", Name: "upper"})
	l.v.Live(&client.ToolProgress{RunID: "run1", CallID: "c1", Name: "upper", Partial: "50%"})
	l.v.Live(&client.ToolOpened{RunID: "run1", CallID: "c2", Name: "child", Parent: "c1"})
	m := l.v.Model()
	if c := m.Rows[0].Call; c.State != CallRunning || c.Partial != "50%" || len(c.Children) != 1 || c.Children[0].Name != "child" {
		t.Fatalf("running: %+v", c)
	}
	l.v.Live(&client.ToolFinished{RunID: "run1", CallID: "c1", Name: "upper", Result: "A"})
	if c := l.v.Model().Rows[0].Call; c.State != CallEnded || c.Output != "A" || c.Committed {
		t.Fatalf("ended, not committed: %+v", c)
	}

	// The call item lands: the row is the record's now, with the live
	// call state kept until the output lands.
	l.append(itemEntry(call, "resp1"))
	m = l.v.Model()
	if len(m.Rows) != 1 || m.Rows[0].Live || m.Rows[0].Call.State != CallEnded || m.Rows[0].Call.Output != "A" {
		t.Fatalf("call landed: %+v / %+v", m.Rows, m.Rows[0].Call)
	}
	l.append(itemEntry(openresponses.NewFunctionCallOutput("c1", "A"), ""))
	m = l.v.Model()
	if len(m.Rows) != 2 || !m.Rows[0].Call.Committed || len(m.Rows[0].Call.Children) != 0 {
		t.Fatalf("output landed: %+v / %+v", m.Rows, m.Rows[0].Call)
	}
	// And a late event for the answered call is stale.
	l.v.Live(&client.ToolFinished{RunID: "run1", CallID: "c1", Name: "upper", Result: "A"})
	if len(l.v.calls) != 0 {
		t.Fatal("a late tool event revived an answered call")
	}
}

func TestPermissionsLifecycle(t *testing.T) {
	l := newLog(t)
	call := &openresponses.FunctionCall{ID: "fc1", CallID: "c1", Name: "rm", Arguments: `{}`}
	l.v.Live(&client.RunStarted{RunID: "run1"})
	l.stream("run1", "resp1", call, true)
	l.append(itemEntry(call, "resp1"))
	l.v.Live(&client.ToolOpened{RunID: "run1", CallID: "c1", Name: "rm", Args: `{}`})
	l.v.Live(&client.ToolFinished{RunID: "run1", CallID: "c1", Name: "rm", Deferred: true, Reason: "delete files?"})
	if m := l.v.Model(); len(m.Permissions) != 1 || m.Permissions[0].Question != "delete files?" || m.Rows[0].Call.State != CallDeferred {
		t.Fatalf("deferred: %+v", l.v.Model())
	}
	l.v.Live(&client.RunEnded{RunID: "run1", Reason: agentturn.ReasonInputRequired,
		Pending: []client.Pending{{CallID: "c1", Name: "rm", Args: `{}`, Reason: agentturn.PendingDeferred}}})
	l.append(agentsession.NewRunEnd("run1", "input_required", "", []string{"c1"}))
	m := l.v.Model()
	if m.Turn.State != RequiresAction || len(m.Permissions) != 1 || m.Permissions[0].Question != "delete files?" {
		t.Fatalf("after the run: %+v", m)
	}
	if m.Rows[0].Call.State != CallDeferred {
		t.Fatalf("a deferred call must outlive its run's flush: %+v", m.Rows[0].Call)
	}

	// The answer's run starts: nothing waits any more.
	l.v.Live(&client.RunStarted{RunID: "run2", Resume: true})
	if m := l.v.Model(); len(m.Permissions) != 0 || m.Turn.State != Running {
		t.Fatalf("resumed: %+v", m)
	}
}

func TestPermissionClearedByTheOutputOfAnotherSource(t *testing.T) {
	l := newLog(t)
	call := &openresponses.FunctionCall{ID: "fc1", CallID: "c1", Name: "rm", Arguments: `{}`}
	l.append(itemEntry(call, "resp1"))
	l.v.Live(&client.RunEnded{RunID: "run1", Reason: agentturn.ReasonInputRequired,
		Pending: []client.Pending{{CallID: "c1", Name: "rm", Reason: agentturn.PendingDeferred}}})
	if m := l.v.Model(); m.Turn.State != RequiresAction {
		t.Fatalf("turn = %v", m.Turn.State)
	}
	// Another process answered it: the record has the output.
	l.append(itemEntry(openresponses.NewFunctionCallOutput("c1", "no"), ""))
	if m := l.v.Model(); len(m.Permissions) != 0 || m.Turn.State != Idle {
		t.Fatalf("after the output landed: %+v", m)
	}
}

func TestModelRetryDropsTheFailedTurnsItems(t *testing.T) {
	l := newLog(t)
	l.v.Live(&client.RunStarted{RunID: "run1"})
	l.v.Live(&client.TurnStarted{RunID: "run1", Turn: 1, Model: "m"})
	l.stream("run1", "resp1", msg("m1", "assistant", "partial"), false)
	l.v.Live(&client.ModelRetrying{RunID: "run1", Turn: 1, Attempt: 1})
	m := l.v.Model()
	if liveRows(m) != 0 || m.Turn.Attempt != 1 {
		t.Fatalf("after the retry: %+v, turn %+v", m.Rows, m.Turn)
	}
	l.v.Live(&client.TurnStarted{RunID: "run1", Turn: 1, Model: "m"})
	if l.v.Model().Turn.Attempt != 0 {
		t.Fatal("the attempt count outlived the retry")
	}
}

func TestResetRebuildsAndPrunesTheOverlay(t *testing.T) {
	l := newLog(t)
	l.v.Live(&client.RunStarted{RunID: "run1"})
	kept := l.append(itemEntry(msg("m0", "user", "kept"), ""))
	_ = kept
	l.append(itemEntry(msg("m1", "assistant", "lost on reset"), "resp1"))
	l.stream("run1", "resp2", msg("m2", "assistant", "in flight"), false)
	l.stream("run1", "resp2", msg("m3", "assistant", "lands in the new log"), false)
	if m := l.v.Model(); len(m.Rows) != 4 {
		t.Fatalf("before the reset: %d rows", len(m.Rows))
	}

	// The log was replaced: m1 is gone, m3 is there, m2 is not yet.
	fresh := agentsession.New(agentsession.Header{})
	for _, e := range []agentsession.Entry{
		itemEntry(msg("m0", "user", "kept"), ""),
		itemEntry(msg("m3", "assistant", "lands in the new log"), "resp2"),
	} {
		if _, err := fresh.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	l.v.Record(agentsession.Change{Kind: agentsession.Reset, Session: fresh})
	m := l.v.Model()
	if len(m.Rows) != 3 {
		t.Fatalf("after the reset: %d rows, want m0, m3 and the overlay m2", len(m.Rows))
	}
	if m.Rows[2].Live != true || client.ItemID(m.Rows[2].Item) != "m2" || m.Rows[1].Live {
		t.Fatalf("rows after the reset: %+v", m.Rows)
	}
	// And the overlay's own entry still replaces it afterwards.
	l.s = fresh
	l.append(itemEntry(msg("m2", "assistant", "in flight"), "resp2"))
	if m := l.v.Model(); liveRows(m) != 0 || len(m.Rows) != 3 {
		t.Fatalf("after m2 landed: %+v", m.Rows)
	}
}

func TestHeadSwitchesThePath(t *testing.T) {
	l := newLog(t)
	root := l.append(itemEntry(msg("a", "user", "root"), ""))
	trunk := l.append(itemEntry(msg("b", "assistant", "trunk"), "r"))
	side := itemEntry(msg("c", "user", "side"), "")
	side.Parent = root
	sid := l.append(side)
	// The writer's session does not move its leaf for an append under
	// an earlier entry; a follower's, reading, does, and the view takes
	// whichever the change's session says.
	m := l.v.Model()
	if m.Leaf != trunk || len(m.Leaves) != 2 {
		t.Fatalf("after the branch: leaf %s, leaves %v", m.Leaf, m.Leaves)
	}
	if err := l.s.Branch(sid); err != nil {
		t.Fatal(err)
	}
	l.v.Record(agentsession.Change{Kind: agentsession.Head, Session: l.s, Leaf: sid})
	m = l.v.Model()
	if m.Leaf != sid || len(m.Rows) != 2 || client.ItemID(m.Rows[1].Item) != "c" {
		t.Fatalf("on the side: %+v", m.Rows)
	}
	if err := l.s.Branch(trunk); err != nil {
		t.Fatal(err)
	}
	l.v.Record(agentsession.Change{Kind: agentsession.Head, Session: l.s, Leaf: trunk})
	m = l.v.Model()
	if m.Leaf != trunk || len(m.Rows) != 2 || client.ItemID(m.Rows[1].Item) != "b" {
		t.Fatalf("on the trunk: %+v", m.Rows)
	}
}

func TestHiddenItemsAreNotRows(t *testing.T) {
	l := newLog(t)
	e := itemEntry(msg("h", "user", "hidden"), "")
	no := false
	e.Visible = &no
	l.append(e)
	if m := l.v.Model(); len(m.Rows) != 0 {
		t.Fatalf("rows = %+v", m.Rows)
	}
}

// marked is the custom entry the recorder writes for an item the filter
// keeps from the model: the item, and the response that produced it.
func marked(t *testing.T, item openresponses.Item, resp string) *agentsession.CustomEntry {
	t.Helper()
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := json.Marshal(resp)
	return &agentsession.CustomEntry{NS: item.ItemType(), Data: data,
		EntryBase: agentsession.EntryBase{Unknown: map[string]json.RawMessage{session.ResponseIDMember: id}}}
}

// An item kept from the model is on the record, so it is a row; it is
// flagged, since it is not part of what the model saw.
func TestItemKeptFromTheModelIsARow(t *testing.T) {
	l := newLog(t)
	l.v.Live(&client.RunStarted{RunID: "run1"})
	r := &openresponses.ReasoningItem{ID: "rs1"}
	l.stream("run1", "resp1", r, true)
	if m := l.v.Model(); len(m.Rows) != 1 || !m.Rows[0].Live {
		t.Fatalf("before the entry: %+v", m.Rows)
	}
	l.append(marked(t, r, "resp1"))
	m := l.v.Model()
	if len(m.Rows) != 1 || m.Rows[0].Live || m.Rows[0].EntryID == "" || !m.Rows[0].KeptFromModel {
		t.Fatalf("after the entry: %+v", m.Rows)
	}
	if m.Rows[0].ResponseID != "resp1" {
		t.Errorf("response = %q", m.Rows[0].ResponseID)
	}
}

// A later response that reuses an item ID is another item. Until its
// stream names the response the row has no response ID, and that must not
// make it match what an earlier response landed.
func TestReusedItemIDIsNotLandedBeforeTheStreamNamesItsResponse(t *testing.T) {
	l := newLog(t)
	l.v.Live(&client.RunStarted{RunID: "run1"})
	l.v.Live(&client.TurnStarted{RunID: "run1", Turn: 1})
	l.stream("run1", "resp1", msg("msg_0", "assistant", "first"), true)
	l.append(itemEntry(msg("msg_0", "assistant", "first"), "resp1"))
	l.v.Live(&client.ResponseCompleted{RunID: "run1", ResponseID: "resp1"})

	l.v.Live(&client.TurnStarted{RunID: "run1", Turn: 2})
	l.stream("run1", "", msg("msg_0", "assistant", "second"), false)
	m := l.v.Model()
	if len(m.Rows) != 2 || !m.Rows[1].Live || text(m.Rows[1].Item) != "second" {
		t.Fatalf("the reused ID was hidden: %+v", m.Rows)
	}
	// The stream names its response, then the entry lands: one row each.
	l.stream("run1", "resp2", msg("msg_0", "assistant", "second"), true)
	if m := l.v.Model(); len(m.Rows) != 2 {
		t.Fatalf("naming the response duplicated the row: %+v", m.Rows)
	}
	l.append(itemEntry(msg("msg_0", "assistant", "second"), "resp2"))
	if m := l.v.Model(); len(m.Rows) != 2 || liveRows(m) != 0 {
		t.Fatalf("after the second entry: %+v", m.Rows)
	}
}

// An entry drops the overlay of its own response, not of another's.
func TestEntryDoesNotDropAnotherResponsesOverlay(t *testing.T) {
	l := newLog(t)
	l.v.Live(&client.RunStarted{RunID: "run1"})
	l.stream("run1", "resp2", msg("msg_0", "assistant", "second"), false)
	l.append(itemEntry(msg("msg_0", "assistant", "first"), "resp1"))
	m := l.v.Model()
	if len(m.Rows) != 2 || !m.Rows[1].Live {
		t.Fatalf("resp1's entry dropped resp2's overlay: %+v", m.Rows)
	}
}

// The other order: the entry of an unnamed item lands before the live
// stream has said anything. Its response is still open, so the entry is
// the item's, and the late live events leave no second row.
func TestUnnamedItemWhoseEntryLandedFirst(t *testing.T) {
	l := newLog(t)
	l.v.Live(&client.RunStarted{RunID: "run1"})
	l.v.Live(&client.TurnStarted{RunID: "run1", Turn: 1})
	l.append(itemEntry(msg("msg_0", "assistant", "hi"), "resp1"))
	l.stream("run1", "", msg("msg_0", "assistant", "hi"), true)
	if m := l.v.Model(); len(m.Rows) != 1 || liveRows(m) != 0 {
		t.Fatalf("ghost: %+v", m.Rows)
	}
	l.v.Live(&client.ResponseCompleted{RunID: "run1", ResponseID: "resp1"})
	if m := l.v.Model(); len(m.Rows) != 1 {
		t.Fatalf("after response end: %+v", m.Rows)
	}
}

func text(item openresponses.Item) string {
	if m, ok := item.(*openresponses.Message); ok {
		return m.Text()
	}
	return ""
}

// A bookmark label appended last is bookkeeping. The branch it sits on
// still ends at the newest item, and the leaf is never the label.
func TestBookmarkLabelDoesNotHideTheTip(t *testing.T) {
	l := newLog(t)
	a := l.append(itemEntry(msg("a", "user", "a"), ""))
	l.append(itemEntry(msg("b", "assistant", "b"), "r"))
	c := l.append(itemEntry(msg("c", "user", "c"), ""))
	l.append(agentsession.NewLabelEntry(a, "bookmark"))
	m := l.v.Model()
	if len(m.Leaves) != 1 || m.Leaves[0] != c {
		t.Errorf("leaves = %v, want [%s]", m.Leaves, c)
	}
	if m.Leaf != c || len(m.Rows) != 3 {
		t.Errorf("leaf = %s with %d rows, want %s with 3", m.Leaf, len(m.Rows), c)
	}
	found := false
	for _, id := range m.Leaves {
		found = found || id == m.Leaf
	}
	if !found {
		t.Errorf("the leaf %s is not among the leaves %v", m.Leaf, m.Leaves)
	}
}

// Only a deferred call of a run that ended input_required is a question.
// A call an abort or a failure cut off is shown as cut off.
func TestCutOffCallsAreNotPermissions(t *testing.T) {
	l := newLog(t)
	call := &openresponses.FunctionCall{ID: "fc1", CallID: "c1", Name: "rm", Arguments: `{}`}
	l.v.Live(&client.RunStarted{RunID: "run1"})
	l.append(itemEntry(call, "resp1"))
	l.v.Live(&client.RunEnded{RunID: "run1", Reason: agentturn.ReasonAborted,
		Pending: []client.Pending{{CallID: "c1", Name: "rm", Args: `{}`, Reason: agentturn.PendingAborted}}})
	m := l.v.Model()
	if m.Turn.State != Idle || len(m.Permissions) != 0 {
		t.Fatalf("turn %v with permissions %+v, want idle and none", m.Turn.State, m.Permissions)
	}
	if len(m.CutOff) != 1 || m.CutOff[0].CallID != "c1" || m.CutOff[0].Reason != string(agentturn.PendingAborted) {
		t.Fatalf("cut off = %+v", m.CutOff)
	}
	if m.Rows[0].Call.State != CallCutOff {
		t.Errorf("call state = %v, want cut off", m.Rows[0].Call.State)
	}
	// The next run answers or abandons them: nothing is cut off then.
	l.v.Live(&client.RunStarted{RunID: "run2"})
	if m := l.v.Model(); len(m.CutOff) != 0 || m.Rows[0].Call.State == CallCutOff {
		t.Errorf("cut off after the next run started: %+v", m.CutOff)
	}
}

func TestOnlyDeferredCallsOfAnInputRequiredRunAreQuestions(t *testing.T) {
	l := newLog(t)
	l.v.Live(&client.RunStarted{RunID: "run1"})
	l.v.Live(&client.RunEnded{RunID: "run1", Reason: agentturn.ReasonInputRequired, Pending: []client.Pending{
		{CallID: "c1", Name: "rm", Reason: agentturn.PendingDeferred},
		{CallID: "c2", Name: "ls", Reason: agentturn.PendingAborted},
	}})
	m := l.v.Model()
	if len(m.Permissions) != 1 || m.Permissions[0].CallID != "c1" || len(m.CutOff) != 1 || m.CutOff[0].CallID != "c2" || m.Turn.State != RequiresAction {
		t.Fatalf("permissions %+v, cut off %+v, turn %v", m.Permissions, m.CutOff, m.Turn.State)
	}
}
