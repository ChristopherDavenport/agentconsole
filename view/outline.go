package view

import (
	"strings"

	"github.com/ChristopherDavenport/openresponses"
)

// BlockKind says what a [Block] of the outline is.
type BlockKind int

// Block kinds.
const (
	// BlockTurn is rows that belong together: one model response's, with
	// the outputs the harness sent back for its calls, or a single row
	// that has no response.
	BlockTurn BlockKind = iota
	// BlockGroup is a run of quiet calls, which a client may show as one
	// line.
	BlockGroup
)

// ToolCount is how many calls of a tool a group holds.
type ToolCount struct {
	Name string
	N    int
}

// Group is what a group block says of its calls.
type Group struct {
	// Calls are the indices into [Model.Rows] of the group's call rows,
	// in order. [Block.Rows] holds them and the rows between them.
	Calls []int
	// Tools are the tool names of the calls in first-use order, with how
	// many calls each has.
	Tools []ToolCount
	// FirstTurn and LastTurn are the turns the group spans, as [Block.Turn]
	// numbers them.
	FirstTurn, LastTurn int
}

// Turns is how many turns the group spans.
func (g Group) Turns() int { return g.LastTurn - g.FirstTurn + 1 }

// Block is a part of the outline: the rows of [Model.Rows] cut into
// stretches in order, every row in exactly one block.
type Block struct {
	Kind BlockKind
	// Rows are the indices into [Model.Rows] the block holds, in order
	// and contiguous. A group's members are its calls, the reasoning
	// between them and the outputs of its calls.
	Rows []int
	// ResponseID is the response a turn block's rows share, "" for a row
	// that has none.
	ResponseID string
	// Turn is the position in the run of the response a turn block
	// belongs to, counted from 1 in the order the responses first appear
	// in the rows, 0 for a block with no response.
	Turn int
	// Group is set on a group block.
	Group *Group
}

// Outline cuts a model's rows into blocks: turns, and groups of quiet
// calls. It is a pure projection: it reads the model and keeps nothing,
// and Model.Rows is as it was.
//
// A turn is one model response: the rows that share a ResponseID, the
// outputs sent back for its calls following them. A group is a run of
// two or more quiet calls: calls that ended, whose output is on the
// record, empty, that did not fail and need nothing from the person
// (no question, hold, withdrawal, cut-off or verdict but proceed).
// Reasoning between quiet calls joins the run and so do the outputs of
// its calls; anything else ends it. A run crosses turns. A live call is
// never quiet, so a running call is never in a group.
func Outline(m Model) []Block {
	turns := map[string]int{}
	turnOf := func(row Row) int {
		if row.ResponseID == "" {
			return 0
		}
		if _, ok := turns[row.ResponseID]; !ok {
			turns[row.ResponseID] = len(turns) + 1
		}
		return turns[row.ResponseID]
	}
	for _, row := range m.Rows {
		turnOf(row)
	}
	asked := map[string]bool{}
	for _, p := range m.Permissions {
		asked[p.CallID] = true
	}
	for _, p := range m.CutOff {
		asked[p.CallID] = true
	}
	for _, q := range m.Questions {
		asked[q.CallID] = true
	}
	var out []Block
	// emit adds rows as turn blocks: contiguous rows of one response
	// together, an output with the block of the call it answers.
	answers := map[string]string{} // call ID -> response of its call row
	emit := func(idx ...int) {
		for _, i := range idx {
			row := m.Rows[i]
			if row.Call != nil {
				answers[row.Call.CallID] = row.ResponseID
			}
			resp := row.ResponseID
			if o, ok := row.Item.(*openresponses.FunctionCallOutput); ok && resp == "" {
				resp = answers[o.CallID]
			}
			if n := len(out); n > 0 && out[n-1].Kind == BlockTurn && resp != "" && out[n-1].ResponseID == resp {
				out[n-1].Rows = append(out[n-1].Rows, i)
				continue
			}
			out = append(out, Block{Kind: BlockTurn, Rows: []int{i}, ResponseID: resp, Turn: turns[resp]})
		}
	}
	var run, pending, calls []int
	members := map[string]bool{}
	closeRun := func() {
		if len(calls) >= 2 {
			g := &Group{Calls: calls, FirstTurn: turnOf(m.Rows[calls[0]]), LastTurn: turnOf(m.Rows[calls[len(calls)-1]])}
			at := map[string]int{}
			for _, i := range calls {
				name := m.Rows[i].Call.Name
				if j, ok := at[name]; ok {
					g.Tools[j].N++
					continue
				}
				at[name] = len(g.Tools)
				g.Tools = append(g.Tools, ToolCount{Name: name, N: 1})
			}
			out = append(out, Block{Kind: BlockGroup, Rows: run, Group: g})
		} else {
			emit(run...)
		}
		emit(pending...)
		run, pending, calls, members = nil, nil, nil, map[string]bool{}
	}
	for i, row := range m.Rows {
		switch it := row.Item.(type) {
		case *openresponses.FunctionCall:
			if quiet(row, asked) {
				run = append(append(run, pending...), i)
				pending = nil
				calls = append(calls, i)
				members[row.Call.CallID] = true
				continue
			}
		case *openresponses.FunctionCallOutput:
			if members[it.CallID] && row.Call == nil && !row.Live {
				run = append(append(run, pending...), i)
				pending = nil
				continue
			}
		case *openresponses.ReasoningItem:
			if len(run) > 0 && !row.Live && row.Fold == nil && !row.Hidden {
				pending = append(pending, i)
				continue
			}
		}
		closeRun()
		emit(i)
	}
	closeRun()
	return out
}

// quiet reports whether a call row says nothing and needs nothing.
func quiet(row Row, asked map[string]bool) bool {
	c := row.Call
	if c == nil || row.Live || row.Hidden || row.Fold != nil {
		return false
	}
	if c.State != CallEnded || !c.Committed || c.Failed || c.HeldBeside || c.Withdrawn != "" || c.Reason != "" || len(c.Children) > 0 {
		return false
	}
	if c.Verdict != "" && c.Verdict != "proceed" {
		return false
	}
	if asked[c.CallID] || row.LeftOut != "" {
		return false
	}
	return strings.TrimSpace(c.Output) == ""
}
