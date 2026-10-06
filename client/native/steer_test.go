package native_test

import (
	"context"
	"testing"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/view"
)

// queuedTexts are the texts of the inputs the model lists as waiting.
func queuedTexts(m view.Model) []string {
	var out []string
	for _, q := range m.Queued {
		out = append(out, text(q.Item))
	}
	return out
}

// userRows counts the rows of user messages with the text.
func userRows(m view.Model, s string) (n int) {
	for _, row := range m.Rows {
		if msg, ok := row.Item.(*openresponses.Message); ok && msg.Role == openresponses.RoleUser && msg.Text() == s {
			n++
		}
	}
	return n
}

// TestASteerIsQueuedOnTheRecordBeforeTheRunTakesIt steers while a tool is
// held, when the run emits nothing that would carry the agent's queued
// event to the recorder. The steer is on the record when Steer returns,
// listed as queued until the run appends it, and then a row: never both,
// and never neither, at any state the view passed through.
func TestASteerIsQueuedOnTheRecordBeforeTheRunTakesIt(t *testing.T) {
	g := newGates()
	r := newRig(t, agentturn.Config{ModelName: "m", Tools: []agenttool.Tool{upperTool(g)}, Model: &script{responses: []func(context.Context, *openresponses.Emitter) error{
		callTool("call_1", "upper", `{"text":"abc"}`),
		say(nil, nil, "done"),
	}}})
	run := r.prompt("go")
	g.arrive(t, "tool")

	if err := r.ctl.Steer(r.ctx, openresponses.UserText("also this"), openresponses.UserText("and that")); err != nil {
		t.Fatalf("steer: %v", err)
	}
	s, err := r.be.Record().Read(r.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	owed, err := s.PendingQueued(s.Leaf())
	if err != nil {
		t.Fatal(err)
	}
	if len(owed) != 2 || owed[0].Mode != agentsession.ModeSteer || text(owed[0].Item) != "also this" || text(owed[1].Item) != "and that" {
		t.Fatalf("queued on the record when Steer returned: %+v", owed)
	}
	m := r.waitFor("the steers queued", func(m view.Model) bool { return len(m.Queued) == 2 })
	if got := queuedTexts(m); got[0] != "also this" || got[1] != "and that" {
		t.Errorf("queued = %q, want them in the order steered", got)
	}
	if m.Queued[0].Mode != agentsession.ModeSteer || m.Queued[0].EntryID != owed[0].ID {
		t.Errorf("queued[0] = %+v, want the steer's entry %s", m.Queued[0], owed[0].ID)
	}

	g.release("tool")
	r.finish(run)
	m = r.waitFor("the steers taken", func(m view.Model) bool {
		return idle(m) && len(m.Queued) == 0 && userRows(m, "also this") == 1 && userRows(m, "and that") == 1
	})

	r.mu.Lock()
	defer r.mu.Unlock()
	queuedSeen := map[string]bool{}
	for i, h := range r.history {
		for _, s := range []string{"also this", "and that"} {
			queued := 0
			for _, q := range queuedTexts(h) {
				if q == s {
					queued++
				}
			}
			rows := userRows(h, s)
			if queued > 0 {
				queuedSeen[s] = true
			}
			if queued+rows > 1 {
				t.Errorf("state %d shows %q %d times queued and %d as a row", i, s, queued, rows)
			}
			if queuedSeen[s] && queued+rows == 0 {
				t.Errorf("state %d shows %q neither queued nor as a row", i, s)
			}
		}
	}
}
