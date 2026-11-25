package view

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"
)

// call appends a call of tool name in response resp, and, when out is
// not nil, its dispatch and output.
func (l *log) call(resp, id, name string, out *string) {
	l.t.Helper()
	e := l.append(itemEntry(&openresponses.FunctionCall{ID: "fc_" + id, CallID: id, Name: name, Arguments: `{}`}, resp))
	if out != nil {
		l.append(agentsession.NewDispatch(id, e))
		l.append(itemEntry(openresponses.NewFunctionCallOutput(id, *out), ""))
	}
}

func str(s string) *string { return &s }

// shape is the outline as a test reads it: a turn is T<n>(items), a loud
// row r<i>, a quiet run q<rows>, a run of turns R<first>-<last>(turns).
func shape(bs []Block) string {
	var parts []string
	for _, b := range bs {
		parts = append(parts, shapeOf(b))
	}
	return strings.Join(parts, " ")
}

func shapeOf(b Block) string {
	if b.Kind == BlockRun {
		var ts []string
		for _, t := range b.Turns {
			ts = append(ts, shapeOf(t))
		}
		return fmt.Sprintf("R%d-%d(%s)", b.FirstTurn, b.LastTurn, strings.Join(ts, " "))
	}
	var its []string
	for _, it := range b.Items {
		if it.Quiet != nil {
			s := "q"
			for _, r := range it.Quiet.Rows {
				s += fmt.Sprint(r, ".")
			}
			its = append(its, strings.TrimSuffix(s, "."))
		} else {
			its = append(its, fmt.Sprintf("r%d", it.Row))
		}
	}
	return fmt.Sprintf("T%d(%s)", b.Turn, strings.Join(its, " "))
}

func TestAChainOfFullyQuietTurnsIsOneRun(t *testing.T) {
	l := newRecordsLog(t)
	l.append(itemEntry(openresponses.UserText("go"), ""))
	l.call("r1", "c1", "bash", str(""))
	l.call("r2", "c2", "bash", str("  \n"))
	l.call("r3", "c3", "read", str(""))
	l.call("r3", "c4", "bash", str(""))
	m := l.v.Model()
	bs := Outline(m)
	// rows: 0 user, then each call followed by its output: calls 1, 3, 5, 7
	if got, want := shape(bs), "T0(r0) R1-3(T1(q1.2) T2(q3.4) T3(q5.6.7.8))"; got != want {
		t.Fatalf("outline = %s, want %s", got, want)
	}
	run := bs[1]
	if want := []ToolCount{{"bash", 3}, {"read", 1}}; !reflect.DeepEqual(run.Tools, want) {
		t.Errorf("tools = %+v, want %+v", run.Tools, want)
	}
	if !reflect.DeepEqual(run.Calls, []int{1, 3, 5, 7}) || run.FirstTurn != 1 || run.LastTurn != 3 {
		t.Errorf("run = %+v", run)
	}
	if again := Outline(m); !reflect.DeepEqual(bs, again) {
		t.Errorf("Outline is not the same twice")
	}
}

func TestOutlineLeavesTheRowsAloneAndPartitionsThem(t *testing.T) {
	l := newRecordsLog(t)
	l.append(itemEntry(openresponses.UserText("go"), ""))
	l.call("r1", "c1", "bash", str(""))
	l.call("r2", "c2", "bash", str(""))
	l.call("r3", "c3", "bash", str("out"))
	l.append(itemEntry(msg("m1", "assistant", "done"), "r4"))
	m := l.v.Model()
	before := append([]Row(nil), m.Rows...)
	var rows []int
	for _, b := range Outline(m) {
		rows = append(rows, b.Rows()...)
	}
	if !reflect.DeepEqual(before, m.Rows) {
		t.Error("Outline changed Rows")
	}
	for i, r := range rows {
		if r != i {
			t.Fatalf("the blocks hold rows %v, want each row once in order", rows)
		}
	}
	if len(rows) != len(m.Rows) {
		t.Errorf("%d rows in blocks, %d in the model", len(rows), len(m.Rows))
	}
}

func TestALoneQuietTurnIsATurnWithOneQuietRun(t *testing.T) {
	l := newRecordsLog(t)
	l.call("r1", "c1", "bash", str(""))
	l.call("r2", "c2", "bash", str("hello"))
	if got, want := shape(Outline(l.v.Model())), "T1(q0.1) T2(r2 r3)"; got != want {
		t.Errorf("outline = %s, want %s", got, want)
	}
}

func TestQuietLoudQuietKeepsItsOrder(t *testing.T) {
	l := newRecordsLog(t)
	l.call("r1", "ca", "bash", str(""))
	l.call("r1", "cb", "bash", str("hello"))
	l.call("r1", "cc", "read", str(""))
	m := l.v.Model()
	// 0 ca, 1 out, 2 cb, 3 out, 4 cc, 5 out
	if got, want := shape(Outline(m)), "T1(q0.1 r2 r3 q4.5)"; got != want {
		t.Errorf("outline = %s, want %s", got, want)
	}
}

func TestATurnWithAMessageIsNeverInARun(t *testing.T) {
	l := newRecordsLog(t)
	l.append(itemEntry(msg("m1", "assistant", "starting"), "r1"))
	l.call("r1", "c1", "bash", str(""))
	l.call("r2", "c2", "bash", str(""))
	l.call("r3", "c3", "bash", str(""))
	// 0 message, 1 c1, 2 out, 3 c2, 4 out, 5 c3, 6 out
	if got, want := shape(Outline(l.v.Model())), "T1(r0 q1.2) R2-3(T2(q3.4) T3(q5.6))"; got != want {
		t.Errorf("outline = %s, want %s", got, want)
	}
}

func TestReasoningAroundQuietCallsJoinsTheirRun(t *testing.T) {
	l := newRecordsLog(t)
	l.append(itemEntry(&openresponses.ReasoningItem{ID: "rs1"}, "r1"))
	l.call("r1", "c1", "bash", str(""))
	l.append(itemEntry(&openresponses.ReasoningItem{ID: "rs2"}, "r1"))
	l.call("r1", "c2", "bash", str(""))
	l.append(itemEntry(&openresponses.ReasoningItem{ID: "rs3"}, "r1"))
	l.append(itemEntry(&openresponses.ReasoningItem{ID: "rs4"}, "r2"))
	l.append(itemEntry(msg("m1", "assistant", "done"), "r2"))
	// 0 rs1, 1 c1, 2 out, 3 rs2, 4 c2, 5 out, 6 rs3 | 7 rs4, 8 message
	if got, want := shape(Outline(l.v.Model())), "T1(q0.1.2.3.4.5.6) T2(r7 r8)"; got != want {
		t.Errorf("outline = %s, want %s", got, want)
	}
}

func TestACallThatSaysSomethingIsLoud(t *testing.T) {
	l := newRecordsLog(t)
	l.call("r1", "c1", "bash", str(""))
	// A failed call.
	e := l.append(itemEntry(&openresponses.FunctionCall{ID: "fc_f", CallID: "cf", Name: "bash", Arguments: `{}`}, "r1"))
	l.append(agentsession.NewDispatch("cf", e))
	out := itemEntry(openresponses.NewFunctionCallOutput("cf", ""), "")
	out.Failed = true
	l.append(out)
	// A held call.
	h := l.append(itemEntry(&openresponses.FunctionCall{ID: "fc_h", CallID: "ch", Name: "bash", Arguments: `{}`}, "r1"))
	d := agentsession.NewDecision("ch", h, agentsession.VerdictHold, "policy")
	d.Reason = "asks"
	l.append(d)
	l.call("r1", "c2", "bash", str(""))
	// 0 c1, 1 out, 2 cf, 3 out, 4 ch, 5 c2, 6 out
	if got, want := shape(Outline(l.v.Model())), "T1(q0.1 r2 r3 r4 q5.6)"; got != want {
		t.Errorf("outline = %s, want %s", got, want)
	}
}

func TestARunningCallIsNeverQuietNorInARun(t *testing.T) {
	l := newRecordsLog(t)
	l.call("r1", "c1", "bash", str(""))
	l.call("r2", "c2", "bash", str(""))
	l.v.Live(&agentturn.RunStart{RunID: "run1"})
	l.stream("run1", "r3", &openresponses.FunctionCall{ID: "fc_3", CallID: "c3", Name: "bash", Arguments: `{}`}, true)
	l.v.Live(&agentturn.ToolStart{RunID: "run1", CallID: "c3", Name: "bash"})
	if got, want := shape(Outline(l.v.Model())), "R1-2(T1(q0.1) T2(q2.3)) T3(r4)"; got != want {
		t.Fatalf("outline = %s, want %s", got, want)
	}
	// It ends, but its output is not on the record yet: still a loud row,
	// and its turn is not in the run.
	l.v.Live(&agentturn.ToolEnd{RunID: "run1", CallID: "c3", Name: "bash", Result: agenttool.Text("")})
	if got, want := shape(Outline(l.v.Model())), "R1-2(T1(q0.1) T2(q2.3)) T3(r4)"; got != want {
		t.Errorf("ended, uncommitted: outline = %s, want %s", got, want)
	}
}

func TestAnOutputWithoutItsCallRowIsItsOwnBlock(t *testing.T) {
	l := newRecordsLog(t)
	l.append(itemEntry(openresponses.NewFunctionCallOutput("gone", ""), ""))
	if got, want := shape(Outline(l.v.Model())), "T0(r0)"; got != want {
		t.Errorf("outline = %s, want %s", got, want)
	}
}
