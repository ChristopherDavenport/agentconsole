package view

import (
	"fmt"
	"testing"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/openresponses"
)

// hiddenLog is a session with an input the host queued hidden under the
// person's trigger, and one drained hidden, read by a view made with
// opts.
func hiddenLog(t *testing.T, opts ...Option) *log {
	t.Helper()
	l := &log{t: t, s: agentsession.New(agentsession.Header{}), v: New(opts...)}
	l.v.Record(agentsession.Change{Kind: agentsession.Snapshot, Session: l.s})
	no := false
	l.append(agentsession.NewRunStart("run1", agentsession.SourceInput, ""))
	l.append(itemEntry(openresponses.UserText("go"), ""))
	drained := itemEntry(openresponses.UserText("a reminder the host sent"), "")
	drained.Visible = &no
	l.append(drained)
	q := agentsession.NewQueued(openresponses.UserText("owed: the tests failed"), agentsession.ModeSteer)
	q.Visible = &no
	q.Trigger = &agentsession.Trigger{Kind: agentsession.TriggerHuman}
	l.append(q)
	return l
}

// A hidden input is listed only with WithHidden, marked hidden and as
// the host's, whatever its trigger says or if it has none (#47).
func TestHiddenInputsAreListedOnlyWithTheSwitch(t *testing.T) {
	m := hiddenLog(t).v.Model()
	if len(m.Queued) != 0 || rowsWithText(m, "a reminder the host sent") != 0 {
		t.Fatalf("without the switch: queued %v, rows %+v", queuedOf(m), m.Rows)
	}

	m = hiddenLog(t, WithHidden()).v.Model()
	if got := queuedOf(m); fmt.Sprint(got) != "[steer:owed: the tests failed]" {
		t.Fatalf("queued with the switch = %v", got)
	}
	if q := m.Queued[0]; !q.Hidden || q.Words == agentsession.WordsPerson {
		t.Errorf("the hidden queued input = %+v, want it hidden and not the person's", q)
	}
	var row *Row
	for i := range m.Rows {
		if text(m.Rows[i].Item) == "a reminder the host sent" {
			row = &m.Rows[i]
		}
	}
	// It has no trigger, so it claims nothing; hidden, it is the host's
	// all the same, never "unclaimed" (l6-6c L1).
	if row == nil || !row.Hidden || row.Words != agentsession.WordsComposed {
		t.Errorf("the hidden row = %+v, want it listed, hidden and composed", row)
	}
	for _, r := range m.Rows {
		if text(r.Item) == "go" && r.Hidden {
			t.Errorf("a visible row is marked hidden: %+v", r)
		}
	}
}

// A branch the view is not on is marked with the calls its line left
// owed an output: those its last run's end lists as pending, and those a
// run that never ended dispatched. Moving there is what shows them;
// the tree says they are there (#47).
func TestABranchWithOpenCallsIsMarked(t *testing.T) {
	l := newLog(t)
	l.append(agentsession.NewRunStart("run1", agentsession.SourceInput, ""))
	u := l.append(itemEntry(msg("u1", "user", "go"), ""))
	call := l.append(itemEntry(&openresponses.FunctionCall{ID: "fc1", CallID: "c1", Name: "rm", Arguments: `{}`}, "resp1"))
	l.append(agentsession.NewDecision("c1", call, agentsession.VerdictHold, "policy"))
	l.append(agentsession.NewRunEnd("run1", agentsession.ReasonInputRequired, "", []string{"c1"}))

	// A second branch from u: a run that dispatched a call and never
	// ended (the process stopped).
	// Each entry names its parent: appending moves no head.
	start := agentsession.NewRunStart("run2", agentsession.SourceInput, "")
	start.Parent = u
	sr := l.append(start)
	ce := itemEntry(&openresponses.FunctionCall{ID: "fc2", CallID: "c2", Name: "deploy", Arguments: `{}`}, "resp2")
	ce.Parent = sr
	call2 := l.append(ce)
	de := agentsession.NewDispatch("c2", call2)
	de.Parent = call2
	dispatch := l.append(de)

	// A third, where the head goes: nothing open.
	side := itemEntry(msg("s1", "user", "side"), "")
	side.Parent = u
	sid := l.append(side)
	if err := l.s.Branch(sid); err != nil {
		t.Fatal(err)
	}
	l.v.Record(agentsession.Change{Kind: agentsession.Head, Session: l.s, Leaf: sid})

	m := l.v.Model()
	open := map[string]int{}
	for _, b := range m.Branches {
		open[b.Leaf] = b.Open
	}
	if open[call] != 1 || open[call2] != 1 || open[sid] != 0 {
		t.Errorf("open calls by branch = %v, want %s: 1, %s: 1, %s (the view's): 0", open, call, call2, sid)
	}

	// An output on the branch closes it.
	out := itemEntry(openresponses.NewFunctionCallOutput("c2", "deployed"), "")
	out.Parent = dispatch
	oid := l.append(out)
	if err := l.s.Branch(sid); err != nil {
		t.Fatal(err)
	}
	l.v.Record(agentsession.Change{Kind: agentsession.Head, Session: l.s, Leaf: sid})
	for _, b := range l.v.Model().Branches {
		if b.Leaf == oid && b.Open != 0 {
			t.Errorf("the branch whose call has its output = %+v, want none open", b)
		}
	}
}
