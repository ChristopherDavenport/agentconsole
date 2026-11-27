package tui

import (
	"slices"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/toolview"
	"github.com/ChristopherDavenport/agentconsole/view"
)

// bare is a model with the person's outline state empty, to play steps of
// the feed by hand.
func bare() *Model {
	return &Model{flips: map[string]opts{}, quietOpen: map[string]bool{}, turnsFolded: map[string]bool{}}
}

func isStop(m *Model, stop string) bool {
	return stop != "" && slices.Contains(selectable(m.shown(), m.outlineState()), stop)
}

// A cursor on a line (a turn's header, a quiet run's line) is not dropped
// when the model grows and the turn becomes the first of a block: the
// header is the block's, the quiet run the block's, by the same keys.
func TestACursorOnALineFollowsItIntoABlock(t *testing.T) {
	for _, stop := range []string{turnStop + "r2", quietStop + "e_c2"} {
		m := bare()
		m.view = view.Model{Rows: []view.Row{assistantRow("m1", "r1", "hi"), quietRow("c2", "bash", "r2")}}
		m.curEntry = stop
		if !isStop(m, stop) {
			t.Fatalf("%s is not a stop to begin with: %v", stop, selectable(m.shown(), m.outlineState()))
		}
		// Turn 3 settles quiet: turns 2 and 3 are a block, with one header
		// and one quiet line.
		m.view.Rows = append(m.view.Rows, quietRow("c3", "bash", "r3"))
		m.keepCursor()
		if !isStop(m, m.curEntry) {
			t.Errorf("a cursor on %s is %q after the block formed, not a stop of %v", stop, m.curEntry, selectable(m.shown(), m.outlineState()))
		}
	}
}

// The header of a turn inside a block is not drawn: a cursor kept on it
// (a stop from before) goes to the block's header, and there is no stop
// of a run's own.
func TestATurnInsideABlockIsCoveredByTheBlocksHeader(t *testing.T) {
	m := bare()
	m.view = view.Model{Rows: []view.Row{assistantRow("m1", "r1", "hi"), quietRow("c2", "bash", "r2"), quietRow("c3", "bash", "r3")}}
	stops := selectable(m.shown(), m.outlineState())
	if want := []string{turnStop + "r1", "m1", turnStop + "r2", quietStop + "e_c2"}; !slices.Equal(stops, want) {
		t.Fatalf("stops = %v, want %v", stops, want)
	}
	m.curEntry = turnStop + "r3"
	m.keepCursor()
	if m.curEntry != turnStop+"r2" {
		t.Errorf("a cursor on turn 3's header is %q, want the block's header", m.curEntry)
	}
	// A row the quiet run stands for goes to the run's line.
	m.curEntry = "e_c3"
	m.keepCursor()
	if m.curEntry != quietStop+"e_c2" {
		t.Errorf("a cursor on a folded call is %q, want its quiet run's line", m.curEntry)
	}
}

// A fork's selected row that a collapsed line draws as itself is resolved
// to the line's stop when the view opens.
func TestAnOpenedViewSelectsTheStopThatStandsForARow(t *testing.T) {
	m := bare()
	mod := view.Model{Rows: []view.Row{assistantRow("m1", "r1", "hi"), quietRow("c2", "bash", "r2"), quietRow("c3", "bash", "r3")}}
	for _, sel := range []string{"e_c2", "e_c3"} {
		m.Update(openMsg{frozen: &frozen{model: mod, selected: sel}})
		if !isStop(m, m.curEntry) {
			t.Errorf("a view opened on %s has the cursor on %q, not a stop of %v", sel, m.curEntry, selectable(m.shown(), m.outlineState()))
		}
	}
}

// A quiet run that grows at its front keeps the state the person gave it.
func TestARunKeepsItsExpansionWhenItGrowsAtTheFront(t *testing.T) {
	m := bare()
	a := callRow("e_a", view.Call{CallID: "a", Name: "bash", Args: `{}`, State: view.CallRunning})
	b, c := quietRow("b", "bash", "r1"), quietRow("c", "bash", "r1")
	a.ResponseID = "r1"
	m.view = view.Model{Rows: []view.Row{a, b, c}}
	if !m.toggleLine(quietStop + "e_b") {
		t.Fatal("no quiet line to expand")
	}
	for _, it := range layout(m.shown(), m.outlineState()) {
		if it.kind == itemQuiet && !it.open {
			t.Fatalf("the run is not open: %+v", it)
		}
	}
	// A ends: the run is [A, B, C], keyed by A.
	m.view = view.Model{Rows: []view.Row{quietRow("a", "bash", "r1"), b, c}}
	var found bool
	for _, it := range layout(m.shown(), m.outlineState()) {
		if it.kind == itemQuiet {
			found = true
			if !it.open {
				t.Errorf("the run closed under the person when it grew")
			}
		}
	}
	if !found {
		t.Fatal("no quiet run")
	}
}

// Two turns of one response, apart: one header, and the counts are the
// response's.
func TestAResponseWhoseRowsAreApartHasOneHeader(t *testing.T) {
	call := callRow("e_c", view.Call{CallID: "c", Name: "bash", Args: `{}`, State: view.CallEnded, Committed: true, Output: strings.Repeat("x\n", 8), Verdict: "proceed"})
	call.ResponseID = "r1"
	user := view.Row{EntryID: "e_u", Item: openresponses.UserText("wait")}
	out := view.Row{EntryID: "e_o", Item: openresponses.NewFunctionCallOutput("c", "x")}
	m := view.Model{Rows: []view.Row{call, user, out}}
	headers := 0
	for _, it := range layout(m, outlineState{}) {
		if it.kind == itemTurn {
			headers++
			if it.ti == nil || it.ti.calls != 1 {
				t.Errorf("counts are not the response's: %+v", it.ti)
			}
		}
	}
	if headers != 1 {
		t.Errorf("%d headers for one response", headers)
	}
}

// A call the run left unanswered is a call the person must see: its turn
// does not fold.
func TestATurnWithACutOffCallIsNeverFolded(t *testing.T) {
	call := callRow("e_c", view.Call{CallID: "c", Name: "bash", Args: `{}`, State: view.CallCutOff})
	call.ResponseID = "r1"
	m := view.Model{Rows: []view.Row{call}, CutOff: []view.Permission{{CallID: "c", Name: "bash"}}}
	for _, it := range layout(m, outlineState{folded: map[string]bool{"r1": true}}) {
		if it.kind == itemRow {
			return
		}
	}
	t.Errorf("the cut-off call is hidden by a fold")
}

func TestAPressOnABusyTurnSaysSoAndKeepsTheFold(t *testing.T) {
	m := bare()
	running := callRow("e_c", view.Call{CallID: "c", Name: "bash", Args: `{}`, State: view.CallRunning})
	running.ResponseID = "r1"
	m.view = view.Model{Rows: []view.Row{running}}
	m.turnsFolded["r1"] = true
	m.toggleTurn("r1")
	if m.note != "this turn is still going" || !m.turnsFolded["r1"] {
		t.Errorf("note %q, fold kept %v", m.note, m.turnsFolded["r1"])
	}
}

// A head that is only control characters says nothing: the call keeps its
// name and its arguments.
func TestABareHeadThatCleansToNothingFallsBack(t *testing.T) {
	tools := toolview.Renderers{"bash": nothing{}}
	m := view.Model{Rows: []view.Row{callRow("e1", view.Call{CallID: "c1", Name: "bash", Args: `{"command":"ls"}`, State: view.CallEnded, Output: strings.Repeat("x\n", 8), Committed: true})}}
	out, _ := renderRows(m, opts{}, nil, tools, 80, "", "", newMarkdown(), asks{}, outlineState{})
	if got := plain(out); !strings.Contains(got, `○ bash command="ls"`) {
		t.Errorf("no fallback:\n%s", got)
	}
}

type nothing struct{}

func (nothing) Head(view.Call) (toolview.Line, bool) {
	return toolview.Line{toolview.S(toolview.Plain, "\x1b\a\u202e")}, true
}
func (nothing) Body(view.Call, bool) ([]toolview.Line, bool) { return nil, false }
func (nothing) Bare() bool                                   { return true }

// A summarizer's line is one line, cut to the width.
type longSum struct{}

func (longSum) Summarize([]view.Call) (toolview.Line, bool) {
	return toolview.Line{toolview.S(toolview.Plain, strings.Repeat("word ", 60))}, true
}

func TestASummarizersLongLineIsCutToTheWidth(t *testing.T) {
	m := view.Model{Rows: []view.Row{quietRow("c1", "bash", "r1"), quietRow("c2", "bash", "r2")}}
	// Collapsed or expanded, the quiet run's line is the one line that
	// says it.
	for open, want := range map[bool]int{false: 1, true: 1} {
		quiet := map[string]bool{}
		if open {
			quiet["e_c1"] = true
		}
		out, _ := renderRows(m, opts{}, nil, nil, 50, "", "", newMarkdown(), asks{}, outlineState{quiet: quiet, sums: toolview.Summarizers{"bash": longSum{}}})
		n := 0
		for _, l := range strings.Split(plain(out), "\n") {
			if strings.Contains(l, "word") {
				n++
				if lipgloss.Width(strings.TrimRight(l, " ")) > 50 {
					t.Errorf("a line is wider than the screen: %q", l)
				}
			}
		}
		if n != want {
			t.Errorf("open=%v: %d lines say word, want %d (one line each):\n%s", open, n, want, plain(out))
		}
	}
}
