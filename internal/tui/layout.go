package tui

import (
	"fmt"

	"charm.land/lipgloss/v2"

	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/view"
)

// The prefixes of the cursor's stops that are not an entry: a header (a
// turn's, or a run of turns', which is its first turn's) and a quiet
// run's line.
const (
	turnStop  = "turn:"  // and the response's ID
	quietStop = "quiet:" // and the first call's entry
)

// itemKind is what one drawn block of the conversation is.
type itemKind int

const (
	itemRow   itemKind = iota // a row of the model
	itemTurn                  // a turn's header, or a run of turns'
	itemQuiet                 // a quiet run's line
)

// item is one drawn block of the conversation, in order: the outline
// laid out as the person has it folded and expanded. The same list gives
// the rendering and the stops the row cursor steps over, so the two
// cannot disagree.
type item struct {
	kind itemKind
	// row is the index in Model.Rows of an itemRow.
	row int
	// quiet is the run of an itemQuiet.
	quiet *view.QuietRun
	// open is set on a line that is expanded.
	open bool
	// turn, resp and ti are the header's turn, response and what it holds;
	// last is the last turn of a run of turns' header, and the turn
	// itself otherwise.
	turn, last int
	resp       string
	ti         *turnInfo
	// group is the header the item is under, counted from 1, and 0 for an
	// item under none.
	group int
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
// the key of its expansion (its stop is quietStop and the key).
func quietKey(m view.Model, q *view.QuietRun) string { return m.Rows[q.Calls[0]].EntryID }

// layout lays the model's outline out.
func layout(m view.Model, gp outlineState) []item {
	blocks := view.Outline(m, view.WithLoud(gp.loud))
	hidden := hiddenOutputs(m)
	info := turnInfos(m, blocks, gp)
	var items []item
	group, groups := 0, 0
	row := func(i int, stop string) {
		if hidden[i] {
			return
		}
		items = append(items, item{kind: itemRow, row: i, stop: stop, group: group})
	}
	headed := map[int]bool{}
	for _, b := range blocks {
		first, last := turnsOf(b)
		group = 0
		if first > 0 {
			ti := info[first]
			groups++
			group = groups
			if !headed[first] {
				for t := first; t <= last; t++ {
					headed[t] = true
				}
				items = append(items, item{kind: itemTurn, turn: first, last: last, resp: b.ResponseID, ti: ti, stop: turnStop + b.ResponseID, group: group})
			}
			if ti != nil && ti.folded {
				continue
			}
		}
		for _, it := range b.Items {
			q := it.Quiet
			if q == nil {
				row(it.Row, m.Rows[it.Row].EntryID)
				continue
			}
			open := gp.quietExpanded(m, q)
			items = append(items, item{kind: itemQuiet, quiet: q, open: open, stop: quietStop + quietKey(m, q), group: group})
			if !open {
				continue
			}
			for _, i := range q.Rows {
				row(i, m.Rows[i].EntryID)
			}
		}
	}
	return items
}

// turnsOf are the first and last turn a block covers: a run's range, a
// turn twice, and 0 for a block with no response.
func turnsOf(b view.Block) (first, last int) {
	if b.Kind == view.BlockRun {
		return b.FirstTurn, b.LastTurn
	}
	return b.Turn, b.Turn
}

// quietExpanded reports whether the person expanded the quiet run.
func (g outlineState) quietExpanded(m view.Model, q *view.QuietRun) bool {
	if g.all {
		return true
	}
	for _, i := range q.Calls {
		if g.quiet[m.Rows[i].EntryID] {
			return true
		}
	}
	return false
}

// turnInfos reads each header off the outline's rows, by turn: a run of
// turns' by its first turn, counting all its turns.
func turnInfos(m view.Model, blocks []view.Block, gp outlineState) map[int]*turnInfo {
	asked := map[string]bool{}
	for _, p := range m.Permissions {
		asked[p.CallID] = true
	}
	for _, q := range m.Questions {
		asked[q.CallID] = true
	}
	for _, p := range m.CutOff {
		asked[p.CallID] = true
	}
	info := map[int]*turnInfo{}
	turn := func(b view.Block) {
		first, _ := turnsOf(b)
		if first == 0 {
			return
		}
		// A response whose rows are apart has several blocks and one set of
		// counts.
		ti := info[first]
		if ti == nil {
			ti = &turnInfo{resp: b.ResponseID}
			info[first] = ti
		}
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
	}
	for _, b := range blocks {
		turn(b)
	}
	for _, ti := range info {
		// A turn that has something live or waiting is never folded.
		ti.folded = !gp.all && gp.folded[ti.resp] && !ti.busy
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
// and apart from it what failed, which is drawn as an error. A run of
// turns (last is past t) names its range and its calls: the rows under
// it say which have answered.
func turnText(t, last int, ti *turnInfo) (text, failed string) {
	text = fmt.Sprintf("turn %d", t)
	if last > t {
		text = fmt.Sprintf("turns %d–%d", t, last)
	}
	switch {
	case ti == nil:
	case last > t:
		text += " · " + plural(ti.calls, "call")
		if ti.failed > 0 {
			failed = fmt.Sprintf(" · %d failed", ti.failed)
		}
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
func turnLine(t, last int, ti *turnInfo, width int) string {
	text, failed := turnText(t, last, ti)
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
