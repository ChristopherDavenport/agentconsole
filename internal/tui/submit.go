package tui

import (
	"context"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/ChristopherDavenport/openresponses"
)

// Line is a line the user submitted in the input.
type Line struct {
	// Text is what was typed, without the space around it.
	Text string
	// Running is whether a run was going when the line was taken up.
	Running bool
}

// Submission is what the model does with a line, as the submit function
// decided: a prompt starts a run, a steer queues into the one going, and
// a reply is shown over the input. A reply may come with either; an
// empty submission does nothing.
type Submission struct {
	Prompt []openresponses.Item
	Steer  []openresponses.Item
	Reply  string
}

// WithSubmit hands each line the user submits to fn, which decides what
// it is. fn runs off the program's goroutine, one line at a time in the
// order they were submitted; its error is shown and the line is given
// back to the input. Without it, a line is a prompt when idle and a
// steer while a run goes.
func WithSubmit(fn func(context.Context, Line) (Submission, error)) Option {
	return func(m *Model) { m.submit = fn }
}

// submitDoneMsg is the submit function's answer about text.
type submitDoneMsg struct {
	text string
	s    Submission
	err  error
}

// errPromptAndSteer is a submission that asks for both: the line would go
// to the model twice.
var errPromptAndSteer = errors.New("both a prompt and a steer")

// errPromptRunning is a prompt while a run goes, which the agent would
// refuse; its refusal would end the busy state of the run that goes.
var errPromptRunning = errors.New("a prompt while a run goes; steer it instead")

// submitted takes a line up through the submit function, or keeps it
// until the line before it is answered.
func (m *Model) submitted(text string) (tea.Model, tea.Cmd) {
	m.waiting = append(m.waiting, text)
	m.err, m.answer = "", nil
	m.relayout()
	if m.handling {
		return m, nil
	}
	return m, m.handleNext()
}

// handleNext hands the oldest waiting line to the submit function. It
// is asked whether a run goes once the line before has been acted on, so
// a prompt the earlier line started makes this one a steer if fn says
// so.
func (m *Model) handleNext() tea.Cmd {
	if len(m.waiting) == 0 {
		return nil
	}
	text := m.waiting[0]
	m.waiting = m.waiting[1:]
	m.handling, m.handled = true, text
	fn, ctx, l := m.submit, m.ctx, Line{Text: text, Running: m.running()}
	m.inflight.Add(1)
	return func() tea.Msg {
		defer m.inflight.Done()
		s, err := fn(ctx, l)
		return submitDoneMsg{text: text, s: s, err: err}
	}
}

// submitDone acts on what the submit function decided, then takes up
// the next waiting line.
func (m *Model) submitDone(msg submitDoneMsg) (tea.Model, tea.Cmd) {
	m.handling, m.handled = false, ""
	err := msg.err
	switch {
	case err != nil:
	case len(msg.s.Prompt) > 0 && len(msg.s.Steer) > 0:
		err = errPromptAndSteer
	case len(msg.s.Prompt) > 0 && m.running():
		err = errPromptRunning
	}
	var cmds []tea.Cmd
	switch {
	case err != nil:
		m.err = "submit: " + err.Error()
		// Nothing was sent: give the line back to edit or send again,
		// unless something else is being typed.
		if m.in.Value() == "" {
			m.in.SetValue(msg.text)
		}
	default:
		if msg.s.Reply != "" {
			m.answer = strings.Split(strings.TrimRight(msg.s.Reply, "\n"), "\n")
		}
		switch {
		case len(msg.s.Prompt) > 0:
			cmds = append(cmds, m.prompt(msg.s.Prompt...))
		case len(msg.s.Steer) > 0:
			cmds = append(cmds, m.steer(msg.text, msg.s.Steer...))
		}
	}
	cmds = append(cmds, m.handleNext())
	m.relayout()
	return m, tea.Batch(cmds...)
}

// prompt starts a run with items.
func (m *Model) prompt(items ...openresponses.Item) tea.Cmd {
	m.busy = true
	ctl, ctx := m.ctl, m.ctx
	m.inflight.Add(1)
	return tea.Batch(func() tea.Msg {
		defer m.inflight.Done()
		return runDoneMsg{err: ctl.Prompt(ctx, items...)}
	}, m.spin())
}

// answerMaxRows is the most rows a reply takes over the input, and a
// third of the screen is the most it takes on a short one.
const answerMaxRows = 12

// submitLines are the lines the submit function has not answered yet,
// and its last reply, one row each and dimmed.
func (m *Model) submitLines() []string {
	if !m.typing() {
		return nil
	}
	var rows []string
	if m.handling {
		rows = append(rows, dimStyle.Render(truncate("  sending: "+m.handled, m.width)))
	}
	for _, l := range m.waiting {
		rows = append(rows, dimStyle.Render(truncate("  waiting: "+l, m.width)))
	}
	limit := max(min(answerMaxRows, m.height/3), 1)
	for i, l := range m.answer {
		if i == limit-1 && len(m.answer) > limit {
			rows = append(rows, dimStyle.Render(truncate("  ... "+itoa(len(m.answer)-i)+" more lines", m.width)))
			break
		}
		rows = append(rows, dimStyle.Render(truncate(l, m.width)))
	}
	return rows
}
