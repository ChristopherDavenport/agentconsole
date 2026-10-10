package view

import (
	"encoding/json"
	"testing"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"
)

// heldBatch plays a batch of two calls where the policy asks about c1
// and only holds c2 beside it, as agentpolicy's hold-for-batch does,
// through the run's end, live and on the record. c1 was dispatched
// before it was held, which the record says too when dispatch is set.
// The record's end entry lands when record is set.
func heldBatch(l *log, record, dispatch bool) {
	asked := &agentturn.ToolDecision{Action: agentturn.Defer, Reason: "ask: bash(rm:*)", Subject: "rm -rf build"}
	held := &agentturn.ToolDecision{Action: agentturn.Defer, Reason: "held for approval: allowed by bash(ls:*)", Held: true}
	c1 := &openresponses.FunctionCall{ID: "fc1", CallID: "c1", Name: "bash", Arguments: `{"command":"ls && rm -rf build"}`}
	c2 := &openresponses.FunctionCall{ID: "fc2", CallID: "c2", Name: "bash", Arguments: `{"command":"ls"}`}
	l.v.Live(&agentturn.RunStart{RunID: "run1"})
	l.stream("run1", "resp1", c1, true)
	l.stream("run1", "resp1", c2, true)
	e1 := l.append(itemEntry(c1, "resp1"))
	e2 := l.append(itemEntry(c2, "resp1"))
	if dispatch {
		l.append(agentsession.NewDispatch("c1", e1))
	}
	l.append(agentsession.NewDecision("c1", e1, agentsession.VerdictHold, "policy").WithReason(asked.Reason))
	l.append(agentsession.NewDecision("c2", e2, agentsession.VerdictHold, "policy").WithReason(held.Reason))
	for _, c := range []struct {
		call *openresponses.FunctionCall
		d    *agentturn.ToolDecision
	}{{c1, asked}, {c2, held}} {
		l.v.Live(&agentturn.ToolStart{RunID: "run1", CallID: c.call.CallID, Name: "bash", Args: json.RawMessage(c.call.Arguments), Decision: c.d})
		l.v.Live(&agentturn.ToolEnd{RunID: "run1", CallID: c.call.CallID, Name: "bash", Deferred: true, Reason: c.d.Reason})
	}
	l.v.Live(&agentturn.RunEnd{RunID: "run1", Reason: agentturn.ReasonInputRequired, Pending: []agentturn.PendingCall{
		{Call: c1, Reason: agentturn.PendingDeferred, Decision: asked, Dispatched: true},
		{Call: c2, Reason: agentturn.PendingDeferred, Decision: held},
	}})
	if record {
		l.append(agentsession.NewRunEnd("run1", agentsession.ReasonInputRequired, "", []string{"c1", "c2"}))
	}
}

// TestAHeldCallIsNotAskedAboutWhileTheRunsEndIsInHand: of a batch the
// policy asked about one call of and held the other beside it, the view
// asks about the one alone, with the part of the command the question is
// about and the mark that it may have run, from the live call events,
// from the run's end, and once the record's end entry has landed. The
// answers to the batch decide the held call (agentkit's Control releases
// it), so asking would let a refusal override work the policy allowed.
func TestAHeldCallIsNotAskedAboutWhileTheRunsEndIsInHand(t *testing.T) {
	for _, record := range []bool{false, true} {
		l := newLog(t)
		heldBatch(l, record, false)
		m := l.v.Model()
		if m.Turn.State != RequiresAction || len(m.Permissions) != 1 {
			t.Fatalf("record end landed %v: permissions %+v, want c1 alone", record, m.Permissions)
		}
		p := m.Permissions[0]
		if p.CallID != "c1" || p.Question != "ask: bash(rm:*)" || p.Subject != "rm -rf build" || !p.MayHaveRun {
			t.Errorf("record end landed %v: permission = %+v", record, p)
		}
	}
}

// TestAfterARestartEveryPendingCallIsAskedAbout: a view with the record
// alone cannot tell a held call from an asked one, and asks about both;
// the record says which one was dispatched.
func TestAfterARestartEveryPendingCallIsAskedAbout(t *testing.T) {
	l := newLog(t)
	heldBatch(l, true, true)
	v := New()
	v.Record(agentsession.Change{Kind: agentsession.Snapshot, Session: l.s})
	m := v.Model()
	if len(m.Permissions) != 2 {
		t.Fatalf("permissions = %+v, want both calls", m.Permissions)
	}
	if p := m.Permissions[0]; p.CallID != "c1" || p.Question != "ask: bash(rm:*)" || p.Subject != "" || !p.MayHaveRun {
		t.Errorf("c1 from the record: %+v", p)
	}
	if p := m.Permissions[1]; p.CallID != "c2" || p.MayHaveRun {
		t.Errorf("c2 from the record: %+v", p)
	}
}

// TestTheRunsStopCauseIsOnTheTurn: what stopped a run is the live end's,
// and the next run clears it.
func TestTheRunsStopCauseIsOnTheTurn(t *testing.T) {
	l := newLog(t)
	l.v.Live(&agentturn.RunStart{RunID: "run1"})
	l.v.Live(&agentturn.RunEnd{RunID: "run1", Reason: agentturn.ReasonStopped, Cause: agentturn.StopMaxTurns})
	if c := l.v.Model().Turn.Cause; c != agentturn.StopMaxTurns {
		t.Errorf("cause = %q", c)
	}
	l.v.Live(&agentturn.RunStart{RunID: "run2"})
	if c := l.v.Model().Turn.Cause; c != "" {
		t.Errorf("cause after the next start = %q", c)
	}
}

// TestAQuestionIsShownOnceUntilItCloses: a subscriber that attaches
// while a question waits is sent it again, which the view does not
// double; its close takes it off.
func TestAQuestionIsShownOnceUntilItCloses(t *testing.T) {
	l := newLog(t)
	q := &agentturn.Question{ID: "q1", RunID: "run1", Elicitation: agenttool.Elicitation{Message: "go on?"}}
	l.v.Live(q)
	l.v.Live(q)
	if qs := l.v.Model().Questions; len(qs) != 1 || qs[0].Elicitation.Message != "go on?" {
		t.Fatalf("questions = %+v", qs)
	}
	l.v.Live(&agentturn.QuestionClosed{ID: "q1", Answer: &agenttool.Answer{Action: agenttool.ActionAccept}})
	if qs := l.v.Model().Questions; len(qs) != 0 {
		t.Fatalf("questions after the close = %+v", qs)
	}
}
