package tui_test

import (
	"strings"
	"testing"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/view"
)

// write appends entries to the app's session through its store, as
// another writer would, and returns their IDs.
func (a *app) write(es ...agentsession.Entry) []string {
	a.t.Helper()
	var ids []string
	for _, e := range es {
		id, err := a.store.Append(a.ctx, a.rec.SessionID(), e)
		if err != nil {
			a.t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// hiddenQueued is an input the host queued hidden, under the person's
// trigger.
func hiddenQueued() *agentsession.QueuedEntry {
	no := false
	q := agentsession.NewQueued(openresponses.UserText("owed: the tests failed"), agentsession.ModeSteer)
	q.Visible = &no
	q.Trigger = &agentsession.Trigger{Kind: agentsession.TriggerHuman}
	return q
}

// A hidden queued input is listed only with the switch, marked hidden
// and as the host's (#47).
func TestAHiddenQueuedInputShowsOnlyWithTheSwitch(t *testing.T) {
	plain := newViewApp(t, &handSource{}, (&intents{}).act)
	plain.write(agentsession.NewRunStart("run1", agentsession.SourceInput, ""), agentsession.NewItemEntry(openresponses.UserText("go")), hiddenQueued())
	plain.waitFor("the prompt", has("go"))
	if s := plain.screen(); strings.Contains(s, "owed: the tests failed") {
		t.Fatalf("a hidden input shows without the switch:\n%s", s)
	}

	a := newViewAppOf(t, &handSource{}, (&intents{}).act, []view.Option{view.WithHidden()})
	a.write(agentsession.NewRunStart("run1", agentsession.SourceInput, ""), agentsession.NewItemEntry(openresponses.UserText("go")), hiddenQueued())
	s := a.waitFor("the hidden input", has("owed: the tests failed"))
	l := lineWith(s, "owed: the tests failed")
	if !strings.Contains(l, "hidden") || !strings.Contains(l, "not your words") {
		t.Errorf("the hidden input's line = %q, want it marked hidden and not the person's", l)
	}
}

// A hidden message with no trigger reads as the host's, not "user
// (unclaimed)", and the hidden tag goes beside the row's other tags
// rather than in their place (#47).
func TestAHiddenRowIsTheHostsAndKeepsItsTags(t *testing.T) {
	no := false
	copied := agentsession.NewItemEntry(openresponses.UserText("a reminder from elsewhere"))
	copied.Visible = &no
	copied.CopiedFrom = &agentsession.CopiedFrom{Session: "other-session"}
	a := newViewAppOf(t, &handSource{}, (&intents{}).act, []view.Option{view.WithHidden()})
	a.write(agentsession.NewRunStart("run1", agentsession.SourceInput, ""), agentsession.NewItemEntry(openresponses.UserText("go")), copied)
	s := a.waitFor("the hidden row", has("a reminder from elsewhere"))
	i := strings.Index(s, "a reminder from elsewhere")
	// The label and its tags, over as many lines as they wrap to.
	j := strings.LastIndex(s[:i], "(hidden)")
	if j < 0 {
		t.Fatalf("the hidden row is not marked hidden:\n%s", s)
	}
	head := strings.Join(strings.Fields(s[strings.LastIndex(s[:j], "\n")+1:i]), " ")
	if !strings.Contains(head, "composed by the host: not your words") || strings.Contains(head, "unclaimed") {
		t.Errorf("the hidden row's label = %q, want it the host's", head)
	}
	if !strings.Contains(head, "context copied from") {
		t.Errorf("the hidden row's label = %q, want the copy's tag beside hidden", head)
	}
}

// The tree marks a branch the head is not on whose line left a call
// waiting (#47).
func TestTheTreeMarksABranchWithACallWaiting(t *testing.T) {
	a := newViewApp(t, &handSource{}, (&intents{}).act)
	ids := a.write(
		agentsession.NewRunStart("run1", agentsession.SourceInput, ""),
		agentsession.NewItemEntry(openresponses.UserText("clean up")),
	)
	u := ids[1]
	call := a.write(agentsession.NewItemEntry(&openresponses.FunctionCall{CallID: "c1", Name: "rm", Arguments: `{}`}))[0]
	a.write(
		agentsession.NewDecision("c1", call, agentsession.VerdictHold, "policy"),
		agentsession.NewRunEnd("run1", agentsession.ReasonInputRequired, "", []string{"c1"}),
	)
	side := agentsession.NewItemEntry(openresponses.UserText("something else"))
	side.Parent = u
	sid := a.write(side)[0]
	a.write(agentsession.NewLabelEntry(sid, agentsession.LeafLabel))

	a.waitFor("the head on the side branch", all(has("something else"), lacks("Permission requested")))
	a.key("ctrl+t")
	s := a.waitFor("the tree", has("branches of session", "user: something else", "user: clean up"))
	if l := lineWith(s, "user: clean up"); !strings.Contains(l, "1 call waiting") {
		t.Errorf("the branch with the call is not marked: %q", l)
	}
	if l := lineWith(s, "user: something else"); strings.Contains(l, "waiting") {
		t.Errorf("the head's branch is marked: %q", l)
	}
}
