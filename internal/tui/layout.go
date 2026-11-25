package tui

import (
	"fmt"

	"charm.land/lipgloss/v2"

	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/view"
)

// The prefixes of the cursor's stops that are not an entry: a turn's
// header, and a run of turns; the rest is a response's ID.
const (
	turnStop = "turn:"
	runStop  = "run:"
)

// itemKind is what one drawn block of the conversation is.
type itemKind int

const (
	itemRow   itemKind = iota // a row of the model
	itemTurn                  // a turn's header
	itemQuiet                 // a quiet run's line
	itemRun                   // a run of turns' line
)

// item is one drawn block of the conversation, in order: the outline
// laid out as the person has it folded and expanded. The same list gives
// the rendering and the stops the row cursor steps over, so the two
// cannot disagree.
type item struct {
	kind itemKind
	// row is the index in Model.Rows of an itemRow.
	row int
	// quiet is the run of an itemQuiet and block the run of an itemRun.
	quiet *view.QuietRun
	block view.Block
	// open is set on a line that is expanded.
	open bool
	// turn, resp and ti are the header's turn, response and what it holds.
	turn int
	resp string
	ti   *turnInfo
	// stop is the entry the cursor rests on, "" for no stop.
	stop string
}

// turnInfo is what a header says of its turn.
type turnInfo struct {
	resp                   string
	calls, outputs, failed int
	answer, thinking, busy bool
	folded                 bool
}

// quietKey is the entry that stands for a quiet run, its first call's:
// the row cursor's stop, the key of its expansion and the span of its
// line.
func quietKey(m view.Model, q *view.QuietRun) string { return m.Rows[q.Calls[0]].EntryID }

// runKey is the response that stands for a run of turns, its first
// turn's.
func runKey(b view.Block) string { return b.Turns[0].ResponseID }

// layout lays the model's outline out.
func layout(m view.Model, gp grouping) []item {
	blocks := view.Outline(m)
	hidden := hiddenOutputs(m)
	info := turnInfos(m, blocks, gp)
	var items []item
	row := func(i int, stop string) {
		if hidden[i] {
			return
		}
		items = append(items, item{kind: itemRow, row: i, stop: stop})
	}
	turn := func(b view.Block) {
		ti := info[b.Turn]
		if b.Turn > 0 {
			items = append(items, item{kind: itemTurn, turn: b.Turn, resp: b.ResponseID, ti: ti, stop: turnStop + b.ResponseID})
			if ti != nil && ti.folded {
				return
			}
		}
		for _, it := range b.Items {
			q := it.Quiet
			if q == nil {
				row(it.Row, m.Rows[it.Row].EntryID)
				continue
			}
			open := gp.quietExpanded(m, q)
			items = append(items, item{kind: itemQuiet, quiet: q, open: open, stop: quietKey(m, q)})
			if !open {
				continue
			}
			for _, i := range q.Rows {
				stop := m.Rows[i].EntryID
				if i == q.Calls[0] {
					stop = "" // the line is the stop of the first call
				}
				row(i, stop)
			}
		}
	}
	for _, b := range blocks {
		if b.Kind != view.BlockRun {
			turn(b)
			continue
		}
		open := gp.runExpanded(b)
		items = append(items, item{kind: itemRun, block: b, open: open, stop: runStop + runKey(b)})
		if open {
			for _, t := range b.Turns {
				turn(t)
			}
		}
	}
	return items
}

// quietExpanded reports whether the person expanded the quiet run.
func (g grouping) quietExpanded(m view.Model, q *view.QuietRun) bool {
	return g.all || g.quiet[quietKey(m, q)]
}

// runExpanded reports whether the person expanded the run of turns.
func (g grouping) runExpanded(b view.Block) bool { return g.all || g.runs[runKey(b)] }

// turnInfos reads each turn's header off the outline's rows, by turn.
func turnInfos(m view.Model, blocks []view.Block, gp grouping) map[int]*turnInfo {
	asked := map[string]bool{}
	for _, p := range m.Permissions {
		asked[p.CallID] = true
	}
	for _, q := range m.Questions {
		asked[q.CallID] = true
	}
	info := map[int]*turnInfo{}
	var turn func(b view.Block)
	turn = func(b view.Block) {
		if b.Kind == view.BlockRun {
			for _, t := range b.Turns {
				turn(t)
			}
			return
		}
		if b.Turn == 0 {
			return
		}
		ti := &turnInfo{resp: b.ResponseID}
		info[b.Turn] = ti
		for _, i := range b.Rows() {
			row := m.Rows[i]
			if row.Live {
				ti.busy = true
			}
			if msg, ok := row.Item.(*openresponses.Message); ok && msg.Role == openresponses.RoleAssistant {
				ti.answer = true
			}
			if _, ok := row.Item.(*openresponses.ReasoningItem); ok {
				ti.thinking = true
			}
			c := row.Call
			if c == nil {
				continue
			}
			ti.calls++
			switch c.State {
			case view.CallEnded, view.CallReturned, view.CallBlocked:
				ti.outputs++
			case view.CallDeferred, view.CallOpen, view.CallRunning:
				ti.busy = true
			}
			if c.Failed {
				ti.failed++
			}
			if asked[c.CallID] {
				ti.busy = true
			}
		}
		// A turn that has something live or waiting is never folded.
		ti.folded = !gp.all && gp.folded[ti.resp] && !ti.busy
	}
	for _, b := range blocks {
		turn(b)
	}
	return info
}

// plural is n and its noun, in the plural unless n is one.
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// turnText is what a header says: the turn's number and what it holds,
// and apart from it what failed, which is drawn as an error.
func turnText(t int, ti *turnInfo) (text, failed string) {
	text = fmt.Sprintf("turn %d", t)
	switch {
	case ti == nil:
	case ti.calls > 0:
		text += " · " + plural(ti.calls, "call")
		if ti.outputs > 0 {
			text += " → " + plural(ti.outputs, "output")
		}
		if ti.failed > 0 {
			failed = fmt.Sprintf(" · %d failed", ti.failed)
		}
	case ti.answer:
		text += " · answer"
	case ti.thinking:
		text += " · thinking"
	}
	return text, failed
}

// turnLine is a turn's header, one line that never wraps: the turn's
// text dim, a folded turn's marker and hint. No rule: the text alone
// marks the turn. Narrow, the end of the text goes.
func turnLine(t int, ti *turnInfo, width int) string {
	text, failed := turnText(t, ti)
	text, failed = view.CleanLine(text), view.CleanLine(failed)
	hint := ""
	if ti != nil && ti.folded {
		text = "▸ " + text
		hint = " (ctrl+o to expand)"
	}
	full := text + failed + hint
	if lipgloss.Width(full) > width {
		return dimStyle.Render(truncate(full, width))
	}
	line := dimStyle.Render(text)
	if failed != "" {
		line += errStyle.Render(failed)
	}
	if hint != "" {
		line += dimStyle.Render(hint)
	}
	return line
}
