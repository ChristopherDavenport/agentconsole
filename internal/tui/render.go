package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/internal/view"
)

// styles are the few the views use. Colors degrade with the terminal:
// with none, the markers and labels carry the meaning.
var (
	userStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	assistantStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
	dimStyle       = lipgloss.NewStyle().Faint(true)
	toolStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	warnStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	errStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	statusStyle    = lipgloss.NewStyle().Reverse(true)
)

// cursor marks a row that is still streaming.
const cursor = "▍"

// outputLines is how much of a tool's output shows while it is collapsed.
const outputLines = 3

// opts are the rendering switches the user toggles.
type opts struct {
	reasoning bool // reasoning rows expanded
	output    bool // tool arguments and output in full
}

// renderRows renders the conversation, wrapped to width.
func renderRows(m view.Model, o opts, width int) string {
	if width < 10 {
		width = 10
	}
	answered := map[string]bool{}
	for _, row := range m.Rows {
		if row.Call != nil {
			answered[row.Call.CallID] = true
		}
	}
	var blocks []string
	for _, row := range m.Rows {
		if out, ok := row.Item.(*openresponses.FunctionCallOutput); ok && answered[out.CallID] {
			continue // the call's row carries its output
		}
		if b := renderRow(row, o); b != "" {
			blocks = append(blocks, wrap(b, width))
		}
	}
	return strings.Join(blocks, "\n\n")
}

func wrap(s string, width int) string {
	return lipgloss.NewStyle().Width(width).Render(s)
}

func renderRow(row view.Row, o opts) string {
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
		var label string
		switch it.Role {
		case openresponses.RoleUser:
			label = userStyle.Render("you")
		case openresponses.RoleAssistant:
			label = assistantStyle.Render("assistant")
		default:
			label = dimStyle.Render(string(it.Role))
		}
		return label + tag + "\n" + it.Text() + tail
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
			return renderCall(*row.Call, o, tag, 0)
		}
		return toolStyle.Render("⚙ "+it.Name) + tag + " " + it.Arguments
	case *openresponses.FunctionCallOutput:
		return toolStyle.Render("↳ result "+it.CallID) + tag + "\n" + clip(it.Output.String(), o.output)
	case *openresponses.Compaction:
		return dimStyle.Render("[compaction]") + tag
	}
	return dimStyle.Render("["+row.Item.ItemType()+"]") + tag
}

func renderCall(c view.Call, o opts, tag string, depth int) string {
	pad := strings.Repeat("  ", depth)
	head := toolStyle.Render("⚙ "+c.Name) + " [" + c.State.String() + "]" + tag
	var b strings.Builder
	b.WriteString(pad + head)
	if c.Args != "" {
		b.WriteString("\n" + pad + "  args: " + clipLine(c.Args, o.output))
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
			b.WriteString("\n" + pad + "  " + dimStyle.Render("... "+clipLine(c.Partial, o.output)))
		}
	case view.CallBlocked:
		b.WriteString("\n" + pad + "  " + errStyle.Render("blocked: ") + clip(c.Output, o.output))
	case view.CallCutOff:
		b.WriteString("\n" + pad + "  " + warnStyle.Render("cut off before it was answered"))
	case view.CallEnded:
		out := c.Output
		if !c.Committed {
			out += dimStyle.Render(" (live)")
		}
		b.WriteString("\n" + pad + "  ↳ " + indent(clip(out, o.output), pad+"    "))
	}
	if c.Verdict != "" && c.Verdict != "proceed" {
		b.WriteString("\n" + pad + "  " + dimStyle.Render("policy: "+c.Verdict))
	}
	for _, ch := range c.Children {
		b.WriteString("\n" + renderCall(ch, o, "", depth+1))
	}
	return b.String()
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

// statusText is the turn view: the run's state, the model and attempt,
// the turn number.
func statusText(m view.Model, busy, aborting, verified bool) string {
	state := "idle"
	switch {
	case aborting:
		state = "aborting"
	case m.Turn.State == view.Running || busy:
		state = "running"
	case m.Turn.State == view.RequiresAction:
		state = "requires action"
	}
	parts := []string{state}
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
