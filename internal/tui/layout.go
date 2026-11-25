package tui

import (
	"fmt"

	"charm.land/lipgloss/v2"

	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/view"
)

// turnStop is the prefix of the cursor's stop on a turn's header; the
// rest is the response's ID.
const turnStop = "turn:"

// itemKind is what one drawn block of the conversation is.
type itemKind int

const (
	itemRow   itemKind = iota // a row of the model
	itemTurn                  // a turn's header
	itemGroup                 // a collapsed or expanded group's line
)

// item is one drawn block of the conversation, in order: the outline
// laid out as the person has it folded and expanded. The same list gives
// the rendering and the stops the row cursor steps over, so the two
// cannot disagree.
type item struct {
	kind itemKind
	// row is the index in Model.Rows of an itemRow.
	row int
	// blk is the group of an itemGroup.
	blk view.Block
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
	first                  string // the turn's first committed row's entry
}

// layout lays the model's outline out.
func layout(m view.Model, gp grouping) []item {
	blocks := view.Outline(m)
	hidden := hiddenOutputs(m)
	// The turns, numbered as the outline numbers them.
	resp := map[int]string{}
	seenResp := map[string]bool{}
	for _, row := range m.Rows {
		if row.ResponseID != "" && !seenResp[row.ResponseID] {
			seenResp[row.ResponseID] = true
			resp[len(seenResp)] = row.ResponseID
		}
	}
	info := turnInfos(m, blocks, resp, gp)
	var items []item
	// drawn are the turns that have a header; swallowed the turns a
	// collapsed group stands for, which get one only where rows of their
	// own go on after the group (header(t, true)).
	drawn, swallowed := map[int]bool{}, map[int]bool{}
	header := func(t int, rows bool) {
		if t == 0 || drawn[t] || (swallowed[t] && !rows) {
			return
		}
		drawn[t] = true
		items = append(items, item{kind: itemTurn, turn: t, resp: resp[t], ti: info[t], stop: turnStop + resp[t]})
	}
	row := func(i int, stop string) {
		if hidden[i] {
			return
		}
		items = append(items, item{kind: itemRow, row: i, stop: stop})
	}
	for _, blk := range blocks {
		if blk.Kind != view.BlockGroup {
			header(blk.Turn, true)
			if info[blk.Turn] != nil && info[blk.Turn].folded {
				continue
			}
			for _, i := range blk.Rows {
				row(i, m.Rows[i].EntryID)
			}
			continue
		}
		g := blk.Group
		if !gp.expanded(m, blk) {
			folded := true
			for t := g.FirstTurn; t <= g.LastTurn; t++ {
				folded = folded && info[t] != nil && info[t].folded
			}
			if folded {
				// Every turn it spans is folded: their headers stand.
				for t := g.FirstTurn; t <= g.LastTurn; t++ {
					header(t, true)
				}
				continue
			}
			if g.FirstTurn == g.LastTurn {
				// A group inside one turn keeps the turn's header.
				header(g.FirstTurn, true)
			}
			for t := g.FirstTurn; t <= g.LastTurn; t++ {
				swallowed[t] = true // the group's line stands for them
			}
			items = append(items, item{kind: itemGroup, blk: blk, stop: groupKey(m, blk)})
			continue
		}
		if g.FirstTurn == g.LastTurn {
			header(g.FirstTurn, true)
		}
		items = append(items, item{kind: itemGroup, blk: blk, stop: groupKey(m, blk)})
		for j, i := range blk.Rows {
			t := blk.RowTurns[j]
			header(t, true)
			if info[t] != nil && info[t].folded {
				continue
			}
			stop := m.Rows[i].EntryID
			if j == 0 {
				stop = "" // the group's line is the stop of its first member
			}
			row(i, stop)
		}
	}
	return items
}

// turnInfos reads each turn's header off the outline's rows, by turn.
func turnInfos(m view.Model, blocks []view.Block, resp map[int]string, gp grouping) map[int]*turnInfo {
	asked := map[string]bool{}
	for _, p := range m.Permissions {
		asked[p.CallID] = true
	}
	for _, q := range m.Questions {
		asked[q.CallID] = true
	}
	info := map[int]*turnInfo{}
	for _, blk := range blocks {
		for j, i := range blk.Rows {
			t := blk.RowTurns[j]
			if t == 0 {
				continue
			}
			ti := info[t]
			if ti == nil {
				ti = &turnInfo{resp: resp[t]}
				info[t] = ti
			}
			row := m.Rows[i]
			if ti.first == "" {
				ti.first = row.EntryID
			}
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
			case view.CallDeferred:
				ti.busy = true
			case view.CallOpen, view.CallRunning:
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
