package view

import (
	"strings"

	"github.com/ChristopherDavenport/openresponses"
)

// BlockKind says what a [Block] of the outline is.
type BlockKind int

// Block kinds.
const (
	// BlockTurn is one model response: its rows in order, as loud rows and
	// quiet runs. A row that has no response (a user message) is a turn
	// block of its own, numbered 0.
	BlockTurn BlockKind = iota
	// BlockRun is two or more consecutive turns that are quiet through
	// and through, which a client may show as one line.
	BlockRun
)

// ToolCount is how many calls of a tool a run holds.
type ToolCount struct {
	Name string
	N    int
}

// QuietRun is a maximal stretch of a turn's rows made of quiet calls,
// their outputs and the reasoning around them, with at least one call.
// A client shows it as one line, and its rows when expanded.
type QuietRun struct {
	// Rows are the indices into [Model.Rows] of the stretch, in order.
	Rows []int
	// Calls are the call rows among them.
	Calls []int
	// Tools are the tool names of the calls in first-use order, with how
	// many calls each has.
	Tools []ToolCount
}

// Item is one thing a turn holds: a row to show in full, or a quiet run.
type Item struct {
	// Row is the index into [Model.Rows] of a loud row, and unused when
	// Quiet is set.
	Row int
	// Quiet is set on a quiet run.
	Quiet *QuietRun
}

// Rows are the indices of the rows the item holds.
func (it Item) Rows() []int {
	if it.Quiet != nil {
		return it.Quiet.Rows
	}
	return []int{it.Row}
}

// Block is a part of the outline: the model's rows cut into turns, and
// runs of quiet turns, every row in exactly one block, in order.
type Block struct {
	Kind BlockKind
	// Turn is the position in the line of a turn block's response,
	// counted from 1 in the order the responses first appear in the rows,
	// 0 for a block with no response. ResponseID is that response.
	Turn       int
	ResponseID string
	// Items are what a turn block holds, in order.
	Items []Item
	// Turns are the turn blocks a run block stands for, each quiet
	// through and through: every item is a quiet run.
	Turns []Block
	// FirstTurn and LastTurn are the turns a run covers; Calls and Tools
	// are the calls of all its turns, as a QuietRun's are.
	FirstTurn, LastTurn int
	Calls               []int
	Tools               []ToolCount
}

// Rows are the indices of the rows the block holds, in order.
func (b Block) Rows() []int {
	var rows []int
	for _, it := range b.Items {
		rows = append(rows, it.Rows()...)
	}
	for _, t := range b.Turns {
		rows = append(rows, t.Rows()...)
	}
	return rows
}

// quietThrough reports whether a turn block holds only quiet runs.
func (b Block) quietThrough() bool {
	if b.Kind != BlockTurn || b.Turn == 0 || len(b.Items) == 0 {
		return false
	}
	for _, it := range b.Items {
		if it.Quiet == nil {
			return false
		}
	}
	return true
}

// Outline cuts a model's rows into turns, and runs of quiet turns. It is
// a pure projection: it reads the model and keeps nothing, and
// Model.Rows is as it was.
//
// A turn is one model response: the rows that share a ResponseID, with
// the outputs sent back for its calls following them. Inside a turn, each
// maximal stretch of quiet calls, their outputs and the reasoning around
// them is a quiet run, even for one call; every other row is a loud row,
// and the order is kept. A call is quiet when it has ended, its output is
// on the record, it did not fail, its output is empty after trimming, and
// nothing about it needs the person (no question, hold, withdrawal,
// cut-off or verdict but proceed). A live row is never quiet, so a running
// call is never in a run. Two or more consecutive turns whose every item
// is a quiet run are a run of turns.
func Outline(m Model) []Block {
	turns := map[string]int{}
	turnOf := func(resp string) int {
		if resp == "" {
			return 0
		}
		if _, ok := turns[resp]; !ok {
			turns[resp] = len(turns) + 1
		}
		return turns[resp]
	}
	answers := map[string]string{} // call ID -> response of its call row
	for _, row := range m.Rows {
		turnOf(row.ResponseID)
		if row.Call != nil {
			answers[row.Call.CallID] = row.ResponseID
		}
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
	quietCall := map[string]bool{}
	for _, row := range m.Rows {
		if quiet(row, asked) {
			quietCall[row.Call.CallID] = true
		}
	}

	// The turns: contiguous rows of one response, an output with the
	// turn of the call it answers.
	var blocks []Block
	var rows [][]int
	for i, row := range m.Rows {
		resp := row.ResponseID
		if o, ok := row.Item.(*openresponses.FunctionCallOutput); ok && resp == "" {
			resp = answers[o.CallID]
		}
		if n := len(blocks); n > 0 && resp != "" && blocks[n-1].ResponseID == resp {
			rows[n-1] = append(rows[n-1], i)
			continue
		}
		blocks = append(blocks, Block{Kind: BlockTurn, ResponseID: resp, Turn: turns[resp]})
		rows = append(rows, []int{i})
	}
	for k := range blocks {
		blocks[k].Items = items(m, rows[k], quietCall)
	}

	// Runs of turns.
	var out []Block
	for k := 0; k < len(blocks); {
		end := k
		for end < len(blocks) && blocks[end].quietThrough() {
			end++
		}
		if end-k < 2 {
			out = append(out, blocks[k])
			k++
			continue
		}
		run := Block{Kind: BlockRun, Turns: blocks[k:end], FirstTurn: blocks[k].Turn, LastTurn: blocks[end-1].Turn}
		for _, t := range run.Turns {
			for _, it := range t.Items {
				run.Calls = append(run.Calls, it.Quiet.Calls...)
			}
		}
		run.Tools = tools(m, run.Calls)
		out = append(out, run)
		k = end
	}
	return out
}

// items cuts one turn's rows into loud rows and quiet runs.
func items(m Model, rows []int, quietCall map[string]bool) []Item {
	var out []Item
	var stretch, calls []int
	flush := func() {
		if len(calls) > 0 {
			out = append(out, Item{Quiet: &QuietRun{Rows: stretch, Calls: calls, Tools: tools(m, calls)}})
		} else {
			for _, i := range stretch {
				out = append(out, Item{Row: i})
			}
		}
		stretch, calls = nil, nil
	}
	for _, i := range rows {
		row := m.Rows[i]
		member, isCall := false, false
		switch it := row.Item.(type) {
		case *openresponses.FunctionCall:
			member = row.Call != nil && quietCall[row.Call.CallID]
			isCall = member
		case *openresponses.FunctionCallOutput:
			member = row.Call == nil && !row.Live && quietCall[it.CallID]
		case *openresponses.ReasoningItem:
			member = !row.Live && !row.Hidden && row.Fold == nil
		}
		if !member {
			flush()
			out = append(out, Item{Row: i})
			continue
		}
		stretch = append(stretch, i)
		if isCall {
			calls = append(calls, i)
		}
	}
	flush()
	return out
}

// tools counts the calls by tool name in first-use order.
func tools(m Model, calls []int) []ToolCount {
	var out []ToolCount
	at := map[string]int{}
	for _, i := range calls {
		name := m.Rows[i].Call.Name
		if j, ok := at[name]; ok {
			out[j].N++
			continue
		}
		at[name] = len(out)
		out = append(out, ToolCount{Name: name, N: 1})
	}
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
