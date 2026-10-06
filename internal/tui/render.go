package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client"
	"github.com/ChristopherDavenport/agentconsole/view"
)

// styles are the few the views use. Colors degrade with the terminal:
// with none, the markers and labels carry the meaning.
var (
	userStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	assistantStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
	dimStyle       = lipgloss.NewStyle().Faint(true)
	toolStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	runStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	warnStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	errStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	statusStyle    = lipgloss.NewStyle().Reverse(true)
)

// cursor marks a row that is still streaming.
const cursor = "▍"

// outputLines is how much of a tool's output shows while it is collapsed.
const outputLines = 3

// argRunes is how many runes a collapsed row shows of one argument
// value.
const argRunes = 60

// partialLines is how much of a running call's progress shows by
// default: its last lines, the ones a live command is writing, while
// the call goes. A call that has ended shows none of them, only their
// count; Ctrl-O or a click on the row shows the whole window.
const partialLines = 5

// opts are the rendering switches the user toggles: for every row, or, as
// a row's flips, the switches that row has the other way round.
type opts struct {
	reasoning bool // reasoning rows expanded
	output    bool // tool arguments and output in full
}

// flipped is o with the switches set in f the other way round.
func (o opts) flipped(f opts) opts {
	return opts{reasoning: o.reasoning != f.reasoning, output: o.output != f.output}
}

// rowSpan is where a selectable row's block sits in the rendered
// conversation: the lines start..start+height-1.
type rowSpan struct {
	entry         string
	start, height int
}

// hiddenOutputs are the rows whose call's row carries their output, by
// index.
func hiddenOutputs(m view.Model) map[int]bool {
	answered := map[string]bool{}
	for _, row := range m.Rows {
		if row.Call != nil {
			answered[row.Call.CallID] = true
		}
	}
	hidden := map[int]bool{}
	for i, row := range m.Rows {
		if out, ok := row.Item.(*openresponses.FunctionCallOutput); ok && answered[out.CallID] {
			hidden[i] = true
		}
	}
	return hidden
}

// gutter is the width of the cursor's column while a row is selected.
const gutter = 2

// renderRows renders the conversation, wrapped to width, each row with o
// flipped by its entry's flips. With a row selected (sel is its entry),
// every block gets a two-column gutter with a marker on the selected one.
// spin is the spinner's frame, drawn at the right edge of the calls in
// motion. md renders the assistant's messages. The spans say where each committed row's block sits, so the
// viewport can scroll to the selected one and a click can find the row
// under it.
func renderRows(m view.Model, o opts, flips map[string]opts, width int, sel, spin string, md *markdown) (content string, spans []rowSpan) {
	if width < 10 {
		width = 10
	}
	hidden := hiddenOutputs(m)
	w := width
	if sel != "" {
		w = max(width-gutter, 8)
	}
	var blocks []string
	at := 0
	for i, row := range m.Rows {
		if hidden[i] {
			continue
		}
		b := renderRow(row, o.flipped(flips[row.EntryID]), spin, w, md)
		if b == "" {
			continue
		}
		b = wrap(b, w)
		if sel != "" {
			mark := "  "
			if row.EntryID == sel {
				mark = warnStyle.Render("▶") + " "
			}
			ls := strings.Split(b, "\n")
			for j := range ls {
				if j == 0 {
					ls[j] = mark + ls[j]
				} else {
					ls[j] = "  " + ls[j]
				}
			}
			b = strings.Join(ls, "\n")
		}
		h := lipgloss.Height(b)
		if row.EntryID != "" {
			spans = append(spans, rowSpan{entry: row.EntryID, start: at, height: h})
		}
		blocks = append(blocks, b)
		at += h + 1
	}
	md.sweep()
	return strings.Join(blocks, "\n\n"), spans
}

func wrap(s string, width int) string {
	return lipgloss.NewStyle().Width(width).Render(s)
}

func renderRow(row view.Row, o opts, spin string, width int, md *markdown) string {
	if f := row.Fold; f != nil {
		s := fmt.Sprintf("[compaction] context from %s on, summary %d chars", shortID(f.FirstKept), f.SummaryLen)
		if f.Pinned > 0 {
			s += fmt.Sprintf(", %d pinned", f.Pinned)
		}
		return dimStyle.Render(s)
	}
	tag := ""
	switch {
	case row.KeptFromModel:
		tag = dimStyle.Render(" (kept from the model: not sent)")
	case row.Live && row.Open:
		tag = dimStyle.Render(" (streaming)")
	case row.Live:
		tag = dimStyle.Render(" (not committed yet)")
	}
	tail := ""
	if row.Live && row.Open {
		tail = cursor
	}
	switch it := row.Item.(type) {
	case *openresponses.Message:
		// The assistant writes markdown, and is shown it rendered; what
		// the user typed is shown as typed.
		var label string
		body := it.Text()
		switch it.Role {
		case openresponses.RoleUser:
			label = userStyle.Render("you")
		case openresponses.RoleAssistant:
			label = assistantStyle.Render("assistant")
			body = md.render(body, width)
		default:
			label = dimStyle.Render(string(it.Role))
		}
		return label + tag + "\n" + body + tail
	case *openresponses.ReasoningItem:
		text := it.Summary.Text()
		if text == "" {
			text = it.Content.Text()
		}
		if !o.reasoning {
			n := len([]rune(text))
			if row.Live && row.Open {
				return dimStyle.Render(fmt.Sprintf("▸ thinking%s (%d chars, ctrl+r to show)", cursor, n))
			}
			return dimStyle.Render(fmt.Sprintf("▸ reasoning (%d chars, ctrl+r to show)", n)) + tag
		}
		return dimStyle.Render("▾ reasoning"+tail) + tag + "\n" + dimStyle.Render(text)
	case *openresponses.FunctionCall:
		if row.Call != nil {
			return renderCall(*row.Call, o, tag, 0, spin, width)
		}
		return toolStyle.Render("○ "+it.Name) + tag + " " + it.Arguments
	case *openresponses.FunctionCallOutput:
		return toolStyle.Render("↳ result "+it.CallID) + tag + "\n" + clip(it.Output.String(), o.output)
	case *openresponses.Compaction:
		return dimStyle.Render("[compaction]") + tag
	}
	return dimStyle.Render("["+row.Item.ItemType()+"]") + tag
}

// moving is whether a call is in motion: its arguments still streaming,
// or the tool running.
func moving(c view.Call) bool { return c.State == view.CallOpen || c.State == view.CallRunning }

// anyMoving is whether a call of the rows, or one under it, is in motion.
func anyMoving(m view.Model) bool {
	var in func(c view.Call) bool
	in = func(c view.Call) bool {
		if moving(c) {
			return true
		}
		for _, ch := range c.Children {
			if in(ch) {
				return true
			}
		}
		return false
	}
	for _, row := range m.Rows {
		if row.Call != nil && in(*row.Call) {
			return true
		}
	}
	return false
}

// renderCall renders a call and the calls under it, width columns wide.
// Its dot reads as the run line's does: ● in motion, ◆ waiting on a
// permission, ○ at rest, so a call's name starts in the column the run's
// state does. A call in motion is drawn in the run line's color, with
// the spinner at the right edge, under the run line's.
func renderCall(c view.Call, o opts, tag string, depth int, spin string, width int) string {
	pad := strings.Repeat("  ", depth)
	name, state := toolStyle.Render("○ "+c.Name), " ["+c.State.String()+"]"
	switch {
	case moving(c):
		name, state = runStyle.Render("● "+c.Name), runStyle.Render(state)
	case c.State == view.CallDeferred:
		name = warnStyle.Render("◆ " + c.Name)
	}
	// head puts the spinner on the head line of a call in motion.
	head := func(line string) string {
		if moving(c) {
			return atRight(line, runStyle.Render(spin), width)
		}
		return line
	}
	var b strings.Builder
	if o.output {
		// Expanded: the raw arguments on their own line, and the whole
		// output, the way the model saw them.
		b.WriteString(head(pad + name + state + tag))
		if c.Args != "" {
			b.WriteString("\n" + pad + "  args: " + c.Args)
		}
	} else {
		// Collapsed: one line, the name and the arguments compact, the
		// state only while the call has not ended. What the call
		// produced is behind ctrl+o or a second click; the hint says
		// how much of it there is, so a row stays one line however
		// much output it carries.
		line := name
		if c.Args != "" {
			line += " " + compactArgs(c.Args)
		}
		if c.State != view.CallEnded {
			line += state
		}
		if c.State == view.CallEnded && !c.Committed {
			line += dimStyle.Render(" (live)")
		}
		b.WriteString(head(pad + line + tag))
	}
	switch c.State {
	case view.CallDeferred:
		q := c.Reason
		if q == "" {
			q = "waiting for permission"
		}
		b.WriteString("\n" + pad + "  " + warnStyle.Render("? "+q))
	case view.CallRunning:
		if c.Partial != "" {
			// What a running call shows of its progress is its end:
			// the lines it is writing now, not the ones it wrote at
			// the start. Expanded, the whole window the tool last
			// reported.
			n := partialLines
			if o.output {
				n = 0
			}
			b.WriteString("\n" + pad + "  " + dimStyle.Render("..."))
			for _, l := range lastLines(c.Partial, n) {
				l = clipOne(l, o.output)
				b.WriteString("\n" + pad + "    " + dimStyle.Render(l))
			}
		}
	case view.CallBlocked:
		b.WriteString("\n" + pad + "  " + errStyle.Render("blocked: ") + clip(c.Output, o.output))
	case view.CallCutOff:
		b.WriteString("\n" + pad + "  " + warnStyle.Render("cut off before it was answered"))
	case view.CallEnded:
		if o.output {
			out := c.Output
			if !c.Committed {
				out += dimStyle.Render(" (live)")
			}
			b.WriteString("\n" + pad + "  ↳ " + indent(clip(out, o.output), pad+"    "))
		} else if c.Output != "" {
			n := len(strings.Split(strings.TrimRight(c.Output, "\n"), "\n"))
			b.WriteString("\n" + pad + "  " + dimStyle.Render(countHint(n)))
		}
	}
	if c.Verdict != "" && c.Verdict != "proceed" {
		b.WriteString("\n" + pad + "  " + dimStyle.Render("policy: "+c.Verdict))
	}
	for _, ch := range c.Children {
		b.WriteString("\n" + renderCall(ch, o, "", depth+1, spin, width))
	}
	return b.String()
}

// countHint is the collapsed hint of an output's size.
func countHint(n int) string {
	if n == 1 {
		return "· 1 line (ctrl+o)"
	}
	return fmt.Sprintf("· %d lines (ctrl+o)", n)
}

// compactArgs is the arguments on a collapsed row's line: the key=value
// pairs of a JSON object, each value one line, sorted by key so a row
// reads the same however the model ordered them. A value longer than
// argRunes runes is cut, whatever it is: the arguments of a write carry
// a file's whole content. Anything that is not a JSON object is shown
// as it is.
func compactArgs(raw string) string {
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return clipLine(raw, false)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := fmt.Sprint(m[k])
		if r := []rune(v); len(r) > argRunes {
			v = string(r[:argRunes]) + "…"
		}
		parts = append(parts, fmt.Sprintf("%s=%q", k, v))
	}
	return strings.Join(parts, " ")
}

// lastLines is the last n lines of s, in the order they read, with the
// trailing empty ones dropped; n of 0 is all of them. It is what a
// running call shows of its progress, newest last.
func lastLines(s string, n int) []string {
	s = strings.TrimRight(s, "\n")
	lines := strings.Split(s, "\n")
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// clipOne shortens one line of a running call's progress to 120 runes
// unless the row is expanded.
func clipOne(s string, full bool) string {
	if full {
		return s
	}
	if r := []rune(s); len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return s
}

// clip shortens s to a few lines unless full.
func clip(s string, full bool) string {
	s = strings.TrimRight(s, "\n")
	if full {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= outputLines {
		return s
	}
	more := len(lines) - outputLines
	return strings.Join(lines[:outputLines], "\n") + "\n" + dimStyle.Render(fmt.Sprintf("... %d more lines (ctrl+o)", more))
}

// clipLine shortens s to one line unless full.
func clipLine(s string, full bool) string {
	if full {
		return s
	}
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) > 120 {
		return string(r[:120]) + "... (ctrl+o)"
	}
	return s
}

func indent(s, pad string) string {
	return strings.ReplaceAll(s, "\n", "\n"+pad)
}

// runState is how the run stands, for the run line.
type runState int

const (
	stateIdle runState = iota
	stateRunning
	stateAborting
	stateRequiresAction
)

func turnState(m view.Model, busy, aborting bool) runState {
	switch {
	case aborting:
		return stateAborting
	case m.Turn.State == view.Running || busy:
		return stateRunning
	case m.Turn.State == view.RequiresAction:
		return stateRequiresAction
	}
	return stateIdle
}

// runLine is the line over the input (or the hint that takes its place):
// the run's state in words and color, and while the run goes, the
// spinner at the right edge.
func runLine(s runState, spin string, width int) string {
	switch s {
	case stateIdle:
		return dimStyle.Render("○ idle")
	case stateRequiresAction:
		return warnStyle.Render("◆ requires action")
	}
	left := runStyle.Render("● running")
	if s == stateAborting {
		left = warnStyle.Render("● aborting")
	}
	return atRight(left, runStyle.Render(spin), width)
}

// atRight puts spin at the right edge of a width-wide line, so the
// spinners of the run line and of the calls in motion stand in one
// column. A line too long for it to fit beside is wrapped first, and the
// spinner goes on its first row.
func atRight(line, spin string, width int) string {
	room := width - 1 - lipgloss.Width(spin)
	if room < 1 {
		return line
	}
	rows := strings.Split(wrap(line, room), "\n")
	rows[0] = padTo(rows[0], room) + " " + spin
	return strings.Join(rows, "\n")
}

// statusText is the turn view: the model and attempt, the turn number,
// and the session's running token use and cost. The run's state is on
// the run line.
func statusText(m view.Model, verified bool, cost client.Cost) string {
	var parts []string
	if m.Usage.TotalTokens > 0 || m.Usage.InputTokens > 0 || m.Usage.OutputTokens > 0 {
		usage := fmt.Sprintf("tokens %s in, %s out", compactTokens(m.Usage.InputTokens), compactTokens(m.Usage.OutputTokens))
		if cost != nil {
			p := view.Price(m.UsageByModel, cost)
			switch {
			case p.Priced:
				usage += fmt.Sprintf(", $%.4f", p.Total)
			case len(p.Unpriced) > 0:
				usage += fmt.Sprintf(", unpriced: %s", strings.Join(p.Unpriced, ", "))
			}
		}
		parts = append(parts, usage)
	}
	model := m.Turn.Model
	if model == "" {
		model = m.Config
	}
	if model != "" {
		parts = append(parts, "model "+model)
	}
	if m.Turn.Attempt > 0 {
		parts = append(parts, fmt.Sprintf("retry %d", m.Turn.Attempt))
	}
	if m.Turn.Number > 0 {
		parts = append(parts, fmt.Sprintf("turn %d", m.Turn.Number))
	}
	if m.Session != "" {
		s := m.Session
		if len(s) > 12 {
			s = s[:12]
		}
		parts = append(parts, "session "+s)
	}
	if !verified {
		parts = append(parts, "unverified")
	}
	if m.Turn.Withheld {
		parts = append(parts, "last reply withheld")
	}
	return strings.Join(parts, " | ")
}

// compactTokens shortens a token count for the status line: the full count
// in the panes would push the dollar figure past the terminal's edge.
func compactTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}
