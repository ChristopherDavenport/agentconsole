package view

import (
	"fmt"
	"testing"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/openresponses"
)

func queuedOf(m Model) []string {
	var out []string
	for _, q := range m.Queued {
		out = append(out, q.Mode+":"+text(q.Item))
	}
	return out
}

func rowsWithText(m Model, s string) (n int) {
	for _, row := range m.Rows {
		if text(row.Item) == s {
			n++
		}
	}
	return n
}

// A queued entry is listed until the item entry that names it lands, and
// in that same step the item is a row instead.
func TestQueuedInputIsListedUntilItsItemLands(t *testing.T) {
	l := newLog(t)
	l.append(agentsession.NewRunStart("run1", agentsession.SourceInput, ""))
	l.append(itemEntry(openresponses.UserText("go"), ""))
	q1 := l.append(agentsession.NewQueued(openresponses.UserText("also this"), agentsession.ModeSteer))
	l.append(agentsession.NewQueued(openresponses.UserText("later"), agentsession.ModeFollowUp))
	m := l.v.Model()
	if got := queuedOf(m); fmt.Sprint(got) != "[steer:also this followup:later]" {
		t.Fatalf("queued = %v", got)
	}
	if m.Queued[0].EntryID != q1 {
		t.Errorf("queued[0] entry = %s, want %s", m.Queued[0].EntryID, q1)
	}
	if rowsWithText(m, "also this") != 0 {
		t.Error("a queued input is a row before its item lands")
	}
	e := itemEntry(openresponses.UserText("also this"), "")
	e.QueuedFrom = q1
	l.append(e)
	m = l.v.Model()
	if got := queuedOf(m); fmt.Sprint(got) != "[followup:later]" {
		t.Errorf("queued after the steer landed = %v", got)
	}
	if n := rowsWithText(m, "also this"); n != 1 {
		t.Errorf("the steer is %d rows, want 1", n)
	}
}

// A run's end closes what was queued into it, and the recorder writes an
// input the agent still holds again after the end: that one is listed.
func TestRunEndClosesQueuedInputsAndARequeueListsThemAgain(t *testing.T) {
	l := newLog(t)
	l.append(agentsession.NewRunStart("run1", agentsession.SourceInput, ""))
	l.append(itemEntry(openresponses.UserText("go"), ""))
	l.append(agentsession.NewQueued(openresponses.UserText("too late"), agentsession.ModeSteer))
	l.append(agentsession.NewRunEnd("run1", "done", "", nil))
	if m := l.v.Model(); len(m.Queued) != 0 {
		t.Fatalf("queued after the run end = %v", queuedOf(m))
	}
	l.append(agentsession.NewQueued(openresponses.UserText("too late"), agentsession.ModeSteer))
	if got := queuedOf(l.v.Model()); fmt.Sprint(got) != "[steer:too late]" {
		t.Errorf("queued after the requeue = %v", got)
	}
}

// What waits is read on the path to the head, not on the line the view
// runs on through the bookkeeping behind its item: a head moved back to
// an item a steer was queued behind does not list the steer, which the
// line further on took.
func TestHeadMovedBackListsNoQueuedInputBehindIt(t *testing.T) {
	l := newLog(t)
	l.append(agentsession.NewRunStart("run1", agentsession.SourceInput, ""))
	u1 := l.append(itemEntry(openresponses.UserText("go"), ""))
	q := l.append(agentsession.NewQueued(openresponses.UserText("also this"), agentsession.ModeSteer))
	e := itemEntry(openresponses.UserText("also this"), "")
	e.QueuedFrom = q
	l.append(e)
	if err := l.s.Branch(u1); err != nil {
		t.Fatal(err)
	}
	l.v.Record(agentsession.Change{Kind: agentsession.Head, Session: l.s, Leaf: u1})
	if m := l.v.Model(); len(m.Queued) != 0 {
		t.Errorf("queued at the head moved back = %v", queuedOf(m))
	}
}

// The model's queued items are copies: changing one leaves the view's.
func TestQueuedItemsAreCopies(t *testing.T) {
	l := newLog(t)
	l.append(agentsession.NewQueued(openresponses.UserText("a"), agentsession.ModeSteer))
	m := l.v.Model()
	m.Queued[0].Item.(*openresponses.Message).Content[0] = &openresponses.InputText{Text: "changed"}
	if got := text(l.v.Model().Queued[0].Item); got != "a" {
		t.Errorf("the view's queued item changed with the model's: %q", got)
	}
}
