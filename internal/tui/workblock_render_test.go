package tui

import (
	"strings"
	"testing"

	"github.com/ChristopherDavenport/agentconsole/toolview"
	"github.com/ChristopherDavenport/agentconsole/view"
)

// change draws a call as its path and the change it made, two lines, and
// is loud: its output says nothing its row does not.
type change struct{}

func (change) Head(view.Call) (toolview.Line, bool) {
	return toolview.Line{toolview.S(toolview.Emphasis, "a.go")}, true
}

func (change) Body(view.Call, bool) ([]toolview.Line, bool) {
	return []toolview.Line{{toolview.S(toolview.Removed, "- old")}, {toolview.S(toolview.Added, "+ new")}}, true
}
func (change) Loud() bool { return true }

// brokenLoud panics when asked whether it is loud.
type brokenLoud struct{ change }

func (brokenLoud) Loud() bool { panic("loud") }

// editRow is an ended, committed edit in response resp whose output is
// one line.
func editRow(id, resp string) view.Row {
	r := callRow("e_"+id, view.Call{CallID: id, Name: "edit", Args: `{"path":"a.go"}`, State: view.CallEnded, Committed: true, Output: "edited a.go"})
	r.ResponseID = resp
	return r
}

// longRow is an ended, committed bash call in response resp whose output
// is too long to be quiet.
func longRow(id, resp string) view.Row {
	r := callRow("e_"+id, view.Call{CallID: id, Name: "bash", Args: `{"command":"` + id + `"}`, State: view.CallEnded, Committed: true, Output: strings.Repeat("x\n", 8)})
	r.ResponseID = resp
	return r
}

// A loud renderer's call is a row with its body though its output is one
// line, and the quiet calls on either side of it fold as before.
func TestALoudRenderersCallIsNeverFolded(t *testing.T) {
	tools := toolview.Renderers{"edit": change{}}
	m := view.Model{Rows: []view.Row{quietRow("c1", "bash", "r1"), editRow("c2", "r2"), quietRow("c3", "bash", "r3")}}
	out, _ := renderRows(m, opts{}, nil, tools, 80, "", "", newMarkdown(), asks{}, outlineState{loud: loudOf(tools)})
	got := plain(out)
	if !strings.Contains(got, "turns 1–3 · 3 calls") || !strings.Contains(got, "○ edit a.go\n  - old\n  + new") || strings.Count(got, "○ ran 1 bash") != 2 {
		t.Errorf("the edit is not a row with its change between two summaries:\n%s", got)
	}
	// Without the renderer saying so, a short output folds.
	out, _ = renderRows(m, opts{}, nil, tools, 80, "", "", newMarkdown(), asks{}, outlineState{})
	if got := plain(out); !strings.Contains(got, "○ ran 2 bash, 1 edit") || strings.Contains(got, "- old") {
		t.Errorf("a call nothing keeps out did not fold:\n%s", got)
	}
}

// A renderer whose Loud panics is not loud, and one that is not a Loud
// is not either.
func TestARendererWhoseLoudPanicsIsNotLoud(t *testing.T) {
	if isLoud(brokenLoud{}) || isLoud(fileRenderer{}) || !isLoud(change{}) {
		t.Errorf("isLoud: broken %v, plain %v, loud %v", isLoud(brokenLoud{}), isLoud(fileRenderer{}), isLoud(change{}))
	}
	if loudOf(toolview.Renderers{"edit": brokenLoud{}, "read": fileRenderer{}}) != nil {
		t.Errorf("renderers of which none is loud keep a call out")
	}
	tools := toolview.Renderers{"edit": brokenLoud{}}
	m := view.Model{Rows: []view.Row{quietRow("c1", "bash", "r1"), editRow("c2", "r2")}}
	out, _ := renderRows(m, opts{}, nil, tools, 80, "", "", newMarkdown(), asks{}, outlineState{loud: loudOf(tools)})
	if got := plain(out); !strings.Contains(got, "○ ran 1 bash, 1 edit") {
		t.Errorf("the call of a renderer whose Loud panics did not fold:\n%s", got)
	}
}

// Under one header, two calls of one line each have no blank line between
// them, and a call that draws a body stands apart; the spans say where
// each block is, so a click lands on the row under it.
func TestOneLineCallsAreAListAndATallerOneStandsApart(t *testing.T) {
	tools := toolview.Renderers{"edit": change{}}
	m := view.Model{Rows: []view.Row{
		assistantRow("m0", "r0", "on it"),
		longRow("a", "r1"), longRow("b", "r1"), editRow("e", "r2"), longRow("c", "r3"),
		assistantRow("m9", "r4", "done"),
	}}
	for _, sel := range []string{"", "e_b"} {
		out, spans := renderRows(m, opts{}, nil, tools, 80, sel, "", newMarkdown(), asks{}, outlineState{loud: loudOf(tools)})
		lines := strings.Split(plain(out), "\n")
		for i := range lines {
			lines[i] = strings.TrimRight(lines[i], " ")
			if sel != "" && len(lines[i]) >= 2 {
				lines[i] = strings.TrimPrefix(strings.TrimPrefix(lines[i], "▶ "), "  ")
			}
		}
		want := []string{
			"turn 1 · answer",
			"agent",
			"on it",
			"",
			"turns 2–4 · 4 calls",
			`○ bash command="a"  · 8 lines (ctrl+o)`,
			`○ bash command="b"  · 8 lines (ctrl+o)`,
			"",
			"○ edit a.go",
			"  - old",
			"  + new",
			"",
			`○ bash command="c"  · 8 lines (ctrl+o)`,
			"",
			"turn 5 · answer",
			"agent",
			"done",
		}
		if strings.Join(lines, "\n") != strings.Join(want, "\n") {
			t.Fatalf("sel %q: drawn\n%s\nwant\n%s", sel, strings.Join(lines, "\n"), strings.Join(want, "\n"))
		}
		at := map[string][2]int{
			"turn:r0": {0, 1}, "m0": {1, 2}, "turn:r1": {4, 1},
			"e_a": {5, 1}, "e_b": {6, 1}, "e_e": {8, 3}, "e_c": {12, 1},
			"turn:r4": {14, 1}, "m9": {15, 2},
		}
		if len(spans) != len(at) {
			t.Errorf("sel %q: %d spans, want %d: %+v", sel, len(spans), len(at), spans)
		}
		for _, sp := range spans {
			if w, ok := at[sp.entry]; !ok || sp.start != w[0] || sp.height != w[1] {
				t.Errorf("sel %q: span %+v, want start and height %v", sel, sp, w)
			}
		}
	}
}
