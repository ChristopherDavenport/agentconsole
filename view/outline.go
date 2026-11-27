package view

import (
	"strings"
	"unicode/utf8"

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
	// BlockRun is two or more consecutive turns of work: each holds a
	// call and no message of the assistant's, so nothing in them is said
	// to the person. A client shows them under one header that names the
	// range, its rows and quiet runs in order as one turn's are.
	BlockRun
)

// ToolCount is how many calls of a tool a run holds.
type ToolCount struct {
	Name string
	N    int
}

// QuietRun is a maximal stretch of a block's rows made of quiet calls,
// their outputs and the reasoning around them, with at least one call.
// In a run of turns it may cross from one turn into the next. A client
// shows it as one line, and its rows when expanded.
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
// runs of turns of work, every row in exactly one block, in order.
type Block struct {
	Kind BlockKind
	// Turn is the position in the line of a turn block's response,
	// counted from 1 in the order the responses first appear in the rows,
	// 0 for a block with no response (a run block has none either: it is
	// FirstTurn to LastTurn). ResponseID is that response, and a run's
	// first turn's.
	Turn       int
	ResponseID string
	// Items are what the block holds, in order: a turn's rows, or the
	// rows of all a run's turns, cut as one stretch.
	Items []Item
	// Turns are the turn blocks a run block stands for, each with its own
	// Items, for a client that wants the turns apart.
	Turns []Block
	// FirstTurn and LastTurn are the turns a run covers; Calls and Tools
	// are the calls of all its turns, quiet or not, as a QuietRun's are.
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
	return rows
}

// work reports whether a turn's rows are work alone: a call, and no
// message of the assistant's.
func work(m Model, turn int, rows []int) bool {
	if turn == 0 {
		return false
	}
	calls := false
	for _, i := range rows {
		row := m.Rows[i]
		if msg, ok := row.Item.(*openresponses.Message); ok && msg.Role == openresponses.RoleAssistant {
			return false
		}
		calls = calls || row.Call != nil
	}
	return calls
}

// OutlineOption changes how [Outline] cuts a model.
type OutlineOption func(*outlineConfig)

type outlineConfig struct {
	loud func(Call) bool
}

// WithLoud keeps the calls fn reports out of every quiet run, whatever
// their output: a call whose row shows what it did (an edit's diff)
// says something though its output is one line. A nil fn keeps none.
func WithLoud(fn func(Call) bool) OutlineOption {
	return func(c *outlineConfig) { c.loud = fn }
}

// Outline cuts a model's rows into turns, and runs of turns of work. It is
// a pure projection: it reads the model and keeps nothing, and
// Model.Rows is as it was.
//
// A turn is one model response: the rows that share a ResponseID, with
// the outputs sent back for its calls following them. Inside a turn, each
// maximal stretch of quiet calls, their outputs and the reasoning around
// them is a quiet run, even for one call; every other row is a loud row,
// and the order is kept. A call is quiet when it has ended, its output is
// on the record, it did not fail, its output is short (at most 5 lines and 400 runes after trimming),
// nothing about it needs the person (no question, hold, withdrawal,
// cut-off or verdict but proceed, whatever reason that verdict carries)
// and no [WithLoud] keeps it out. A live row is never quiet, so a running
// call is never in a quiet run. Two or more consecutive turns that each
// hold a call and no message of the assistant's are a run of turns: its
// items are cut over all its rows, so a quiet run may cross its turns.
func Outline(m Model, opts ...OutlineOption) []Block {
	var cfg outlineConfig
	for _, o := range opts {
		o(&cfg)
	}
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
	// An output belongs to the nearest call row before it with its call
	// ID: a row's own, since an ID may be reused (a provider's, a copied
	// context) and an empty one names nothing.
	callOf := make([]int, len(m.Rows))
	last := map[string]int{}
	for i, row := range m.Rows {
		turnOf(row.ResponseID)
		callOf[i] = -1
		if row.Call != nil && row.Call.CallID != "" {
			last[row.Call.CallID] = i
		}
		if o, ok := row.Item.(*openresponses.FunctionCallOutput); ok && row.Call == nil && o.CallID != "" {
			if j, ok := last[o.CallID]; ok {
				callOf[i] = j
			}
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
	quietAt := make([]bool, len(m.Rows))
	for i, row := range m.Rows {
		quietAt[i] = row.Call != nil && row.Call.CallID != "" && quiet(row, asked) && (cfg.loud == nil || !cfg.loud(*row.Call))
	}

	// The turns: contiguous rows of one response, an output with the
	// turn of the call it answers.
	var blocks []Block
	var rows [][]int
	for i, row := range m.Rows {
		resp := row.ResponseID
		if callOf[i] >= 0 && resp == "" {
			resp = m.Rows[callOf[i]].ResponseID
		}
		if n := len(blocks); n > 0 && resp != "" && blocks[n-1].ResponseID == resp {
			rows[n-1] = append(rows[n-1], i)
			continue
		}
		blocks = append(blocks, Block{Kind: BlockTurn, ResponseID: resp, Turn: turns[resp]})
		rows = append(rows, []int{i})
	}
	for k := range blocks {
		blocks[k].Items = items(m, rows[k], quietAt, callOf)
	}

	// Runs of turns.
	var out []Block
	for k := 0; k < len(blocks); {
		end := k
		for end < len(blocks) && work(m, blocks[end].Turn, rows[end]) {
			end++
		}
		if end-k < 2 {
			out = append(out, blocks[k])
			k++
			continue
		}
		run := Block{Kind: BlockRun, ResponseID: blocks[k].ResponseID, Turns: blocks[k:end], FirstTurn: blocks[k].Turn, LastTurn: blocks[end-1].Turn}
		var all []int
		for _, rs := range rows[k:end] {
			all = append(all, rs...)
		}
		run.Items = items(m, all, quietAt, callOf)
		for _, i := range all {
			if m.Rows[i].Call != nil {
				run.Calls = append(run.Calls, i)
			}
		}
		run.Tools = tools(m, run.Calls)
		out = append(out, run)
		k = end
	}
	return out
}

// items cuts a block's rows into loud rows and quiet runs.
func items(m Model, rows []int, quietAt []bool, callOf []int) []Item {
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
		switch row.Item.(type) {
		case *openresponses.FunctionCall:
			member = quietAt[i]
			isCall = member
		case *openresponses.FunctionCallOutput:
			member = row.Call == nil && !row.Live && callOf[i] >= 0 && quietAt[callOf[i]]
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

// quiet reports whether a call row says nothing and needs nothing. The
// call's Reason does not count: it is the last decision's whatever its
// verdict, and a rule that allows a call records one ("allowed by
// read"). A held or deferred call is loud by its verdict, HeldBeside or
// a pending question, never by its reason alone.
func quiet(row Row, asked map[string]bool) bool {
	c := row.Call
	if c == nil || row.Live || row.Hidden || row.Fold != nil {
		return false
	}
	if c.State != CallEnded || !c.Committed || c.Failed || c.HeldBeside || c.Withdrawn != "" || len(c.Children) > 0 {
		return false
	}
	if c.Verdict != "" && c.Verdict != "proceed" {
		return false
	}
	if asked[c.CallID] || row.LeftOut != "" {
		return false
	}
	return short(c.Output)
}

// What a quiet call's output may hold: a few lines of the kind the model
// needs and the person only when they look (an ls, a git status).
const (
	quietLines = 5
	quietRunes = 400
)

// short reports whether an output is at most quietLines lines and
// quietRunes runes, after trimming; an empty one is.
func short(out string) bool {
	out = strings.TrimSpace(out)
	return utf8.RuneCountInString(out) <= quietRunes && strings.Count(out, "\n") < quietLines
}
