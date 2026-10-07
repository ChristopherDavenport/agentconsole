package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/toolview"
	"github.com/ChristopherDavenport/agentconsole/view"
)

// fileRenderer draws a call of a tool taking a path as the path, and its
// output as a count of lines, or as the lines themselves expanded. It
// declines arguments it cannot decode.
type fileRenderer struct{}

func (fileRenderer) Head(c view.Call) (toolview.Line, bool) {
	var a struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(c.Args), &a) != nil || a.Path == "" {
		return nil, false
	}
	return toolview.Line{toolview.S(toolview.Emphasis, a.Path)}, true
}

func (fileRenderer) Body(c view.Call, expanded bool) ([]toolview.Line, bool) {
	switch c.State {
	case view.CallEnded:
		if expanded {
			return toolview.Text(toolview.Plain, "file: "+c.Output), true
		}
		return []toolview.Line{{toolview.S(toolview.Dim, "read it")}}, true
	case view.CallDeferred:
		return []toolview.Line{{toolview.S(toolview.Removed, "- old"), toolview.S(toolview.Added, " + new")}}, true
	}
	return nil, false
}

// sloppy declines its body but returns lines with it anyway.
type sloppy struct{}

func (sloppy) Head(view.Call) (toolview.Line, bool) { return nil, false }
func (sloppy) Body(view.Call, bool) ([]toolview.Line, bool) {
	return toolview.Text(toolview.Plain, "not drawn"), false
}

// panicky is a renderer with a bug.
type panicky struct{}

func (panicky) Head(view.Call) (toolview.Line, bool)         { panic("head") }
func (panicky) Body(view.Call, bool) ([]toolview.Line, bool) { panic("body") }

func callRow(id string, c view.Call) view.Row {
	return view.Row{EntryID: id, Item: &openresponses.FunctionCall{CallID: c.CallID, Name: c.Name, Arguments: c.Args}, Call: &c}
}

func TestARendererDrawsItsToolsCalls(t *testing.T) {
	tools := toolview.Renderers{"read": fileRenderer{}, "bad": panicky{}, "grep": sloppy{}}
	m := view.Model{Rows: []view.Row{
		callRow("e1", view.Call{CallID: "c1", Name: "read", Args: `{"path":"a.go"}`, State: view.CallEnded, Output: "x\ny", Committed: true}),
		callRow("e2", view.Call{CallID: "c2", Name: "read", Args: `{"pa`, State: view.CallOpen}),
		callRow("e3", view.Call{CallID: "c3", Name: "bad", Args: `{"k":"v"}`, State: view.CallEnded, Output: "out", Committed: true}),
		callRow("e4", view.Call{CallID: "c4", Name: "read", Args: `{"path":"b.go"}`, State: view.CallDeferred, Reason: "may I?"}),
		callRow("e5", view.Call{CallID: "c5", Name: "grep", Args: `{"pattern":"p"}`, State: view.CallEnded, Output: "hit", Committed: true,
			Children: []view.Call{{CallID: "c6", Name: "read", Args: `{"path":"c.go"}`, State: view.CallRunning, Partial: "going"}}}),
		callRow("e7", view.Call{CallID: "c7", Name: "read", Args: `{"path":"d.go"}`, State: view.CallEnded, Output: "z"}),
	}}
	out, _ := renderRows(m, opts{}, nil, tools, 80, "", "", newMarkdown())
	got := plain(out)
	for _, want := range []string{
		"○ read a.go\n  read it",    // the head for the arguments, the body for the output
		"read {\"pa",                // arguments still streaming: declined, the raw arguments
		`○ bad k="v"`,               // a panic is a decline
		"1 line (ctrl+o)",           // and the client's body with it
		"◆ read b.go [deferred]",    // the client's state after the head
		"  - old + new\n  ? may I?", // the renderer's body, then the client's question
		`○ grep pattern="p"`,        // declined both: as before
		"  ● read c.go [running]",   // a child call by its tool's renderer
		"    ...\n      going",      // whose body declined: the progress tail
		"○ read d.go (live)",        // an output not on the record yet
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, `path="a.go"`) || strings.Contains(got, "2 lines") || strings.Contains(got, "not drawn") {
		t.Errorf("the client's own head or body is drawn beside the renderer's:\n%s", got)
	}

	// Expanded, the raw arguments are shown above the renderer's body,
	// always: what a call does is read from what it was given.
	out, _ = renderRows(m, opts{output: true}, nil, tools, 80, "", "", newMarkdown())
	got = plain(out)
	if !strings.Contains(got, "○ read a.go [ended]\n  args: {\"path\":\"a.go\"}\n  file: x\n  y") {
		t.Errorf("the expanded call is not the head, the arguments and the body:\n%s", got)
	}
	if !strings.Contains(got, "○ read d.go [ended] (live)\n  args: {\"path\":\"d.go\"}\n  file: z") {
		t.Errorf("an expanded call whose output is not on the record does not say so:\n%s", got)
	}
}
