package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

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

// A count that would push its call's line past the width stays under the
// call, whole: a long command does not wrap through it.
func TestACountThatDoesNotFitStaysUnderItsCall(t *testing.T) {
	long := callRow("e_l", view.Call{CallID: "l", Name: "bash", Args: `{"command":"` + strings.Repeat("go test ./... && ", 3) + `true"}`, State: view.CallEnded, Committed: true, Output: strings.Repeat("x\n", 8)})
	long.ResponseID = "r1"
	m := view.Model{Rows: []view.Row{long}}
	wide, _ := renderRows(m, opts{}, nil, nil, 200, "", "", newMarkdown(), asks{}, outlineState{})
	if got := plain(wide); !strings.Contains(got, `true"  · 8 lines (ctrl+o)`) {
		t.Errorf("wide: the count is not on the call's line:\n%s", got)
	}
	narrow, _ := renderRows(m, opts{}, nil, nil, 60, "", "", newMarkdown(), asks{}, outlineState{})
	if got := plain(narrow); !strings.Contains(got, "\n  · 8 lines (ctrl+o)") {
		t.Errorf("narrow: the count is not whole on a line under the call:\n%s", got)
	}
}

// A line a change adds or removes is a band: a tinted background that
// runs to the width, in the tint of the terminal's background, and a
// long line wraps inside it. Context and a line of mixed roles are not.
func TestAddedAndRemovedLinesAreBands(t *testing.T) {
	add := toolview.Line{toolview.S(toolview.Added, "+ new")}
	full, some := colorprofile.TrueColor, colorprofile.ANSI256
	for _, p := range []colorprofile.Profile{full, some} {
		for _, dark := range []bool{true, false} {
			band, ok := bandLine(add, 20, tone{dark: dark, profile: p})
			if !ok || ansi.StringWidth(band) != 20 || ansi.Strip(band) != "+ new"+strings.Repeat(" ", 15) {
				t.Errorf("dark %v: band = %q, ok %v", dark, band, ok)
			}
			gone, _ := bandLine(toolview.Line{toolview.S(toolview.Removed, "- old")}, 20, tone{dark: dark, profile: p})
			if gone == band || ansi.Strip(gone) != "- old"+strings.Repeat(" ", 15) {
				t.Errorf("dark %v: a removed line is drawn as an added one: %q", dark, gone)
			}
		}
	}
	dark, _ := bandLine(add, 20, tone{dark: true, profile: full})
	light, _ := bandLine(add, 20, tone{profile: full})
	if dark == light {
		t.Error("the band is the same on a dark and a light terminal")
	}
	// With 256 colors the tints are the palette's own, not the nearest to
	// the muted ones; with 16 or none there is no band, and the line is
	// green or red text as it was.
	if few, _ := bandLine(add, 20, tone{dark: true, profile: some}); few == dark || !strings.Contains(few, "48;5;22m") {
		t.Errorf("256 colors: band = %q", few)
	}
	for _, p := range []colorprofile.Profile{colorprofile.ANSI, colorprofile.Ascii, colorprofile.NoTTY} {
		if _, ok := bandLine(add, 20, tone{dark: true, profile: p}); ok {
			t.Errorf("profile %v draws a band", p)
		}
	}
	long, _ := bandLine(toolview.Line{toolview.S(toolview.Added, "+ "+strings.Repeat("word ", 10))}, 20, tone{dark: true, profile: full})
	for _, l := range strings.Split(long, "\n") {
		if ansi.StringWidth(l) != 20 {
			t.Errorf("a wrapped band's line is %d wide, want 20: %q", ansi.StringWidth(l), l)
		}
	}
	if strings.Count(long, "\n") == 0 {
		t.Error("a long line did not wrap inside its band")
	}
	for _, l := range []toolview.Line{
		{toolview.S(toolview.Dim, "  context")},
		{toolview.S(toolview.Added, "+1"), toolview.S(toolview.Removed, "−1")},
		{},
	} {
		if _, ok := bandLine(l, 20, tone{dark: true, profile: full}); ok {
			t.Errorf("%q is drawn as a band", l.String())
		}
	}
	// In a call's row the band keeps the body's indent and stays inside
	// the width.
	tools := toolview.Renderers{"edit": change{}}
	m := view.Model{Rows: []view.Row{editRow("c1", "r1")}}
	out, _ := renderRows(m, opts{}, nil, tools, 40, "", "", newMarkdown(), asks{}, outlineState{loud: loudOf(tools)})
	for _, l := range strings.Split(out, "\n") {
		if ansi.StringWidth(l) > 40 {
			t.Errorf("a line is wider than the screen: %q", l)
		}
	}
	if !strings.Contains(ansi.Strip(out), "\n  - old"+strings.Repeat(" ", 33)+"\n  + new") {
		t.Errorf("the bands are not indented under the call and run to the width:\n%q", ansi.Strip(out))
	}
}
