package view

import (
	"reflect"
	"testing"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"
)

// call appends a call of tool name in response resp, and, when out is
// not nil, its output.
func (l *log) call(resp, id, name string, out *string) {
	l.t.Helper()
	e := l.append(itemEntry(&openresponses.FunctionCall{ID: "fc_" + id, CallID: id, Name: name, Arguments: `{}`}, resp))
	if out != nil {
		l.append(agentsession.NewDispatch(id, e))
		l.append(itemEntry(openresponses.NewFunctionCallOutput(id, *out), ""))
	}
}

func str(s string) *string { return &s }

// shape is the outline as a test reads it: "g" and the calls of a group,
// "t" and the rows of a turn.
func shape(bs []Block) string {
	s := ""
	for _, b := range bs {
		if s != "" {
			s += " "
		}
		if b.Kind == BlockGroup {
			s += "G"
		} else {
			s += "T"
		}
		s += "["
		for i, r := range b.Rows {
			if i > 0 {
				s += ","
			}
			s += string(rune('0' + r))
		}
		s += "]"
	}
	return s
}

func TestAChainOfQuietCallsAcrossResponsesIsOneGroup(t *testing.T) {
	l := newRecordsLog(t)
	l.append(itemEntry(openresponses.UserText("go"), ""))
	l.call("r1", "c1", "bash", str(""))
	l.call("r2", "c2", "bash", str("  \n"))
	l.call("r3", "c3", "read", str(""))
	l.call("r3", "c4", "bash", str(""))
	m := l.v.Model()
	bs := Outline(m)
	// rows: 0 user, then each call followed by its output: calls 1, 3, 5, 7
	if got, want := shape(bs), "T[0] G[1,2,3,4,5,6,7,8]"; got != want {
		t.Fatalf("outline = %s, want %s", got, want)
	}
	g := bs[1].Group
	if want := []ToolCount{{"bash", 3}, {"read", 1}}; !reflect.DeepEqual(g.Tools, want) {
		t.Errorf("tools = %+v, want %+v", g.Tools, want)
	}
	if g.FirstTurn != 1 || g.LastTurn != 3 || g.Turns() != 3 {
		t.Errorf("span = %d..%d", g.FirstTurn, g.LastTurn)
	}
	if !reflect.DeepEqual(g.Calls, []int{1, 3, 5, 7}) {
		t.Errorf("calls = %v", g.Calls)
	}
	if again := Outline(m); !reflect.DeepEqual(bs, again) {
		t.Errorf("Outline is not the same twice: %+v vs %+v", bs, again)
	}
}

func TestOutlineLeavesTheRowsAlone(t *testing.T) {
	l := newRecordsLog(t)
	l.call("r1", "c1", "bash", str(""))
	l.call("r1", "c2", "bash", str(""))
	m := l.v.Model()
	before := append([]Row(nil), m.Rows...)
	Outline(m)
	if !reflect.DeepEqual(before, m.Rows) {
		t.Error("Outline changed Rows")
	}
}

func TestACallWithOutputEndsAGroup(t *testing.T) {
	l := newRecordsLog(t)
	l.call("r1", "c1", "bash", str(""))
	l.call("r1", "c2", "bash", str(""))
	l.call("r2", "c3", "bash", str("hello"))
	l.call("r2", "c4", "bash", str(""))
	bs := Outline(l.v.Model())
	// 0 c1, 1 o1, 2 c2, 3 o2, 4 c3, 5 o3, 6 c4, 7 o4
	// The last call is quiet but alone, and shares c3's response: one turn.
	if got, want := shape(bs), "G[0,1,2,3] T[4,5,6,7]"; got != want {
		t.Fatalf("outline = %s, want %s", got, want)
	}
	if bs[1].Turn != 2 {
		t.Errorf("turn = %d", bs[1].Turn)
	}
}

func TestAFailedCallEndsAGroup(t *testing.T) {
	l := newRecordsLog(t)
	l.call("r1", "c1", "bash", str(""))
	l.call("r1", "c2", "bash", str(""))
	e := l.append(itemEntry(&openresponses.FunctionCall{ID: "fc_f", CallID: "cf", Name: "bash", Arguments: `{}`}, "r1"))
	l.append(agentsession.NewDispatch("cf", e))
	out := itemEntry(openresponses.NewFunctionCallOutput("cf", ""), "")
	out.Failed = true
	l.append(out)
	l.call("r1", "c3", "bash", str(""))
	if got, want := shape(Outline(l.v.Model())), "G[0,1,2,3] T[4,5,6,7]"; got != want {
		t.Errorf("outline = %s, want %s", got, want)
	}
}

func TestARunningCallIsNeverInAGroup(t *testing.T) {
	l := newRecordsLog(t)
	l.call("r1", "c1", "bash", str(""))
	l.call("r1", "c2", "bash", str(""))
	l.v.Live(&agentturn.RunStart{RunID: "run1"})
	fc := &openresponses.FunctionCall{ID: "fc_3", CallID: "c3", Name: "bash", Arguments: `{}`}
	l.stream("run1", "r2", fc, true)
	l.v.Live(&agentturn.ToolStart{RunID: "run1", CallID: "c3", Name: "bash"})
	m := l.v.Model()
	if got, want := shape(Outline(m)), "G[0,1,2,3] T[4]"; got != want {
		t.Fatalf("outline = %s, want %s", got, want)
	}
	// It ends, but its output is not on the record yet: still a row.
	l.v.Live(&agentturn.ToolEnd{RunID: "run1", CallID: "c3", Name: "bash", Result: agenttool.Text("")})
	if got, want := shape(Outline(l.v.Model())), "G[0,1,2,3] T[4]"; got != want {
		t.Errorf("ended, uncommitted: outline = %s, want %s", got, want)
	}
}

func TestAHeldCallAndALoneQuietCallAreNotGrouped(t *testing.T) {
	l := newRecordsLog(t)
	l.call("r1", "c1", "bash", str(""))
	e := l.append(itemEntry(&openresponses.FunctionCall{ID: "fc_h", CallID: "ch", Name: "bash", Arguments: `{}`}, "r1"))
	d := agentsession.NewDecision("ch", e, agentsession.VerdictHold, "policy")
	d.Reason = "asks"
	l.append(d)
	l.call("r1", "c2", "bash", str(""))
	m := l.v.Model()
	// 0 c1, 1 o1, 2 ch, 3 c2, 4 o2: three singles, no group.
	for _, b := range Outline(m) {
		if b.Kind == BlockGroup {
			t.Errorf("a group formed: %s", shape(Outline(m)))
		}
	}
}

func TestAMessageEndsAGroupAndReasoningBetweenJoins(t *testing.T) {
	l := newRecordsLog(t)
	l.call("r1", "c1", "bash", str(""))
	l.append(itemEntry(&openresponses.ReasoningItem{ID: "rs1"}, "r2"))
	l.call("r2", "c2", "bash", str(""))
	l.append(itemEntry(&openresponses.ReasoningItem{ID: "rs2"}, "r3"))
	l.append(itemEntry(msg("m1", "assistant", "done"), "r3"))
	l.call("r4", "c3", "bash", str(""))
	m := l.v.Model()
	// 0 c1, 1 o1, 2 reasoning, 3 c2, 4 o2, 5 reasoning, 6 message, 7 c3, 8 o3
	bs := Outline(m)
	if got, want := shape(bs), "G[0,1,2,3,4] T[5,6] T[7,8]"; got != want {
		t.Errorf("outline = %s, want %s", got, want)
	}
}

func TestAnOutputWithoutItsCallRowIsItsOwnBlock(t *testing.T) {
	l := newRecordsLog(t)
	l.append(itemEntry(openresponses.NewFunctionCallOutput("gone", ""), ""))
	bs := Outline(l.v.Model())
	if got, want := shape(bs), "T[0]"; got != want || bs[0].Turn != 0 {
		t.Errorf("outline = %s, want %s", got, want)
	}
}
