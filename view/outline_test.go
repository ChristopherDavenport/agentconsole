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

// lines is n lines of output.
func lines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

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
	l.call("r2", "c2", "bash", str(lines(6)))
	if got, want := shape(Outline(l.v.Model())), "T1(q0.1) T2(r2 r3)"; got != want {
		t.Errorf("outline = %s, want %s", got, want)
	}
}

func TestQuietLoudQuietKeepsItsOrder(t *testing.T) {
	l := newRecordsLog(t)
	l.call("r1", "ca", "bash", str(""))
	l.call("r1", "cb", "bash", str(lines(6)))
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

// Reasoning with text is the model's thinking: a quiet run's line counts
// only calls, so folded into one it would leave no trace on the screen.
// It is a loud row, which splits the run around it and keeps its turn
// out of a run of turns.
func TestReasoningWithTextIsLoud(t *testing.T) {
	said := func(id, text string) *openresponses.ReasoningItem {
		return &openresponses.ReasoningItem{ID: id, Summary: openresponses.Contents{&openresponses.SummaryText{Text: text}}}
	}
	l := newRecordsLog(t)
	l.call("r1", "c1", "bash", str(""))
	l.append(itemEntry(said("rs1", "the test fails on the second case"), "r1"))
	l.call("r1", "c2", "bash", str(""))
	l.append(itemEntry(&openresponses.ReasoningItem{ID: "rs2", Content: openresponses.Contents{&openresponses.ReasoningText{Text: "read the file"}}}, "r2"))
	l.call("r2", "c3", "bash", str(""))
	l.append(itemEntry(said("rs3", " \n"), "r3"))
	l.call("r3", "c4", "bash", str(""))
	l.call("r4", "c5", "bash", str(""))
	// 0 c1, 1 out, 2 rs1, 3 c2, 4 out | 5 rs2, 6 c3, 7 out | 8 rs3 (only
	// space, so quiet), 9 c4, 10 out | 11 c5, 12 out
	if got, want := shape(Outline(l.v.Model())), "T1(q0.1 r2 q3.4) T2(r5 q6.7) R3-4(T3(q8.9.10) T4(q11.12))"; got != want {
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

// A call is quiet by the size of its output: at most 5 lines and 400
// runes after trimming, whatever it says.
func TestAShortOutputIsQuietAndALongOneIsLoud(t *testing.T) {
	for _, tc := range []struct {
		name, out string
		quiet     bool
	}{
		{"empty", "", true},
		{"blank", " \n\n", true},
		{"one line", "a.txt", true},
		{"five lines", lines(5), true}, // a trailing newline is not a line
		{"five lines, no trailing newline", strings.TrimSuffix(lines(5), "\n"), true},
		{"six lines", lines(6), false},
		{"400 runes", strings.Repeat("x", 400), true},
		{"401 runes", strings.Repeat("x", 401), false},
		{"400 multi-byte runes", strings.Repeat("é", 400), true},
		{"401 multi-byte runes", strings.Repeat("é", 401), false},
		{"400 runes and a newline", strings.Repeat("x", 400) + "\n", true},
	} {
		l := newRecordsLog(t)
		l.call("r1", "c1", "bash", str(tc.out))
		bs := Outline(l.v.Model())
		if got := len(bs) == 1 && len(bs[0].Items) == 1 && bs[0].Items[0].Quiet != nil; got != tc.quiet {
			t.Errorf("%s: quiet = %v, want %v (%s)", tc.name, got, tc.quiet, shape(bs))
		}
	}
}

func TestAShortOutputJoinsAQuietRunAndALongOneEndsIt(t *testing.T) {
	l := newRecordsLog(t)
	l.call("r1", "c1", "bash", str(""))
	l.call("r1", "c2", "bash", str("a.txt\nb.txt"))
	l.call("r1", "c3", "bash", str(lines(6)))
	l.call("r1", "c4", "bash", str("ok"))
	if got, want := shape(Outline(l.v.Model())), "T1(q0.1.2.3 r4 r5 q6.7)"; got != want {
		t.Errorf("outline = %s, want %s", got, want)
	}
}

func TestAFailedCallWithAShortOutputIsStillLoud(t *testing.T) {
	l := newRecordsLog(t)
	e := l.append(itemEntry(&openresponses.FunctionCall{ID: "fc_f", CallID: "cf", Name: "bash", Arguments: `{}`}, "r1"))
	l.append(agentsession.NewDispatch("cf", e))
	out := itemEntry(openresponses.NewFunctionCallOutput("cf", "exit 1"), "")
	out.Failed = true
	l.append(out)
	if got, want := shape(Outline(l.v.Model())), "T1(r0 r1)"; got != want {
		t.Errorf("outline = %s, want %s", got, want)
	}
}

// Quietness is a row's, not a call ID's: a provider that reuses an ID per
// response, or leaves it empty, must not let one call's state speak for
// another's.
func TestTwoCallsWithOneIDAreEachTheirOwn(t *testing.T) {
	done := Row{ResponseID: "r1", Item: &openresponses.FunctionCall{CallID: "c", Name: "bash"},
		Call: &Call{CallID: "c", Name: "bash", State: CallEnded, Committed: true, Output: "ok"}}
	running := Row{ResponseID: "r2", Live: true, Item: &openresponses.FunctionCall{CallID: "c", Name: "bash"},
		Call: &Call{CallID: "c", Name: "bash", State: CallRunning}}
	out := Row{Item: openresponses.NewFunctionCallOutput("c", "ok")}
	bs := Outline(Model{Rows: []Row{done, out, running}})
	// 0 call, 1 its output, 2 the live call with the same ID
	if got, want := shape(bs), "T1(q0.1) T2(r2)"; got != want {
		t.Errorf("outline = %s, want %s", got, want)
	}
	// A failed twin behind a quiet one.
	failed := Row{ResponseID: "r2", Item: &openresponses.FunctionCall{CallID: "c", Name: "bash"},
		Call: &Call{CallID: "c", Name: "bash", State: CallEnded, Committed: true, Failed: true}}
	if got, want := shape(Outline(Model{Rows: []Row{done, out, failed}})), "T1(q0.1) T2(r2)"; got != want {
		t.Errorf("failed twin: outline = %s, want %s", got, want)
	}
	// Outputs pair with the nearest call before them: the second output is
	// the second call's, in its turn.
	second := done
	second.ResponseID = "r2"
	out2 := Row{Item: openresponses.NewFunctionCallOutput("c", "ok")}
	if got, want := shape(Outline(Model{Rows: []Row{done, out, second, out2}})), "R1-2(T1(q0.1) T2(q2.3))"; got != want {
		t.Errorf("reused id: outline = %s, want %s", got, want)
	}
}

func TestACallWithNoIDIsLoud(t *testing.T) {
	call := Row{ResponseID: "r1", Item: &openresponses.FunctionCall{Name: "bash"},
		Call: &Call{Name: "bash", State: CallEnded, Committed: true}}
	out := Row{Item: openresponses.NewFunctionCallOutput("", "")}
	if got, want := shape(Outline(Model{Rows: []Row{call, out}})), "T1(r0) T0(r1)"; got != want {
		t.Errorf("outline = %s, want %s", got, want)
	}
}

func TestACallThePolicyAllowedWithAReasonIsQuiet(t *testing.T) {
	l := newRecordsLog(t)
	for _, id := range []string{"c1", "c2"} {
		e := l.append(itemEntry(&openresponses.FunctionCall{ID: "fc_" + id, CallID: id, Name: "read", Arguments: `{}`}, "r1"))
		d := agentsession.NewDecision(id, e, agentsession.VerdictProceed, "policy")
		d.Reason = "allowed by read"
		l.append(d)
		l.append(agentsession.NewDispatch(id, e))
		l.append(itemEntry(openresponses.NewFunctionCallOutput(id, "ok"), ""))
	}
	m := l.v.Model()
	if c := m.Rows[0].Call; c.Verdict != "proceed" || c.Reason != "allowed by read" {
		t.Fatalf("call = %+v, want verdict proceed with the reason", c)
	}
	if got, want := shape(Outline(m)), "T1(q0.1.2.3)"; got != want {
		t.Errorf("outline = %s, want %s", got, want)
	}
}

// A reason makes a call loud only through its verdict: a call with no
// verdict is read as proceed.
func TestAReasonWithoutAVerdictDoesNotMakeACallLoud(t *testing.T) {
	c := func() *Call {
		return &Call{CallID: "c", State: CallEnded, Committed: true, Output: "ok", Reason: "allowed by read"}
	}
	for _, tc := range []struct {
		name  string
		edit  func(*Call)
		asked bool
		quiet bool
	}{
		{"no verdict", func(*Call) {}, false, true},
		{"proceed", func(c *Call) { c.Verdict = "proceed" }, false, true},
		{"hold", func(c *Call) { c.Verdict = "hold" }, false, false},
		{"reject", func(c *Call) { c.Verdict = "reject" }, false, false},
		{"answer", func(c *Call) { c.Verdict = "answer" }, false, false},
		{"held beside", func(c *Call) { c.Verdict = "proceed"; c.HeldBeside = true }, false, false},
		{"answered deferred call, hold on record", func(c *Call) { c.Verdict = "hold"; c.HeldBeside = true }, true, false},
		{"permission entry for it", func(c *Call) { c.Verdict = "proceed" }, true, false},
	} {
		call := c()
		tc.edit(call)
		asked := map[string]bool{}
		if tc.asked {
			asked["c"] = true
		}
		if got := quiet(Row{Call: call}, asked); got != tc.quiet {
			t.Errorf("%s: quiet = %v, want %v", tc.name, got, tc.quiet)
		}
	}
}
