// Package tui is the terminal client: a Bubble Tea model over the view
// reconciler. It renders what [view.Model] says, committed rows plainly
// and in-flight rows marked as such, and turns keys into the backend's
// [client.Control] calls.
//
// # Concurrency
//
// Control.Prompt and Control.Answer block until the run ends, so they run
// in tea.Cmds and report with a [runDoneMsg]. The view is fed by
// [Attach] on goroutines of its own and reaches the model only as
// [ModelMsg] values, which share nothing with the view. The model itself
// is touched by the program's goroutine alone.
package tui

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/internal/client"
	"github.com/ChristopherDavenport/agentconsole/internal/view"
)

// who is what the record says answered a permission.
const who = "human"

// runDoneMsg says a Prompt or Answer returned: the run ended, or never
// started.
type runDoneMsg struct{ err error }

// Model is the Bubble Tea model.
type Model struct {
	ctx      context.Context
	ctl      client.Control
	verified bool

	view view.Model // the latest the feed sent
	vp   viewport.Model
	in   textinput.Model
	o    opts

	width, height int
	ready         bool

	// busy is set from the moment a Prompt or Answer is started until it
	// returns, which the view's own state lags at both ends.
	busy     bool
	aborting bool
	// err is the last run's error, cleared by the next action. feedErr is
	// a stream of the backend that stopped: the view is frozen from then
	// on and nothing the user does revives it, so it is never cleared.
	// It is not restarted: a new feed would start from a new view and a
	// fresh Live subscription that misses what the run emitted meanwhile.
	err     string
	feedErr string

	// inflight counts the Prompt and Answer commands that have not
	// returned, for [Model.Drain].
	inflight sync.WaitGroup

	// decided are the answers given so far to the permissions out, by call.
	decided map[string]agentturn.Answer
	// refusing is set while the reason for a refusal is typed.
	refusing bool
}

var _ tea.Model = (*Model)(nil)

// New returns the model for a backend. The caller starts [Attach] with
// the program's Send; ctx ends the runs the model starts.
func New(ctx context.Context, be client.Backend) *Model {
	in := textinput.New()
	in.Prompt = "> "
	in.Placeholder = "say something"
	in.Focus()
	return &Model{
		ctx:      ctx,
		ctl:      be.Control(),
		verified: be.Record().Verified(),
		vp:       viewport.New(0, 0),
		in:       in,
		decided:  map[string]agentturn.Answer{},
	}
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd { return textinput.Blink }

func (m *Model) running() bool { return m.busy || m.view.Turn.State == view.Running }

// pending is the permission being asked about: the first of the ones out
// that has no answer yet.
func (m *Model) pending() (view.Permission, int, bool) {
	if m.running() {
		return view.Permission{}, 0, false
	}
	for i, p := range m.view.Permissions {
		if _, ok := m.decided[p.CallID]; !ok {
			return p, i, true
		}
	}
	return view.Permission{}, 0, false
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height, m.ready = msg.Width, msg.Height, true
		m.in.Width = max(msg.Width-4, 1)
		m.relayout()
		return m, nil
	case ModelMsg:
		m.view = msg.Model
		m.syncPermissions()
		m.relayout()
		return m, nil
	case FeedErrMsg:
		m.feedErr = msg.Stream + " stream: " + msg.Err.Error()
		m.relayout()
		return m, nil
	case runDoneMsg:
		m.busy, m.aborting = false, false
		// The answers stay: the view may not have caught up with the run
		// yet and still list the permissions just answered. They are
		// dropped when the view stops listing them (syncPermissions), or
		// here when the answer failed and has to be given again.
		if msg.err != nil {
			clear(m.decided)
		}
		m.refusing = false
		m.err = ""
		if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
			m.err = "run: " + msg.err.Error()
		}
		m.in.Focus()
		m.relayout()
		return m, nil
	case tea.MouseMsg:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	case tea.KeyMsg:
		return m.key(msg)
	}
	var cmd tea.Cmd
	m.in, cmd = m.in.Update(msg)
	return m, cmd
}

// syncPermissions forgets answers for calls no longer out: the permission
// came back, or the run moved on.
func (m *Model) syncPermissions() {
	if len(m.decided) == 0 {
		return
	}
	out := map[string]bool{}
	for _, p := range m.view.Permissions {
		out[p.CallID] = true
	}
	for id := range m.decided {
		if !out[id] {
			delete(m.decided, id)
		}
	}
}

// Drain waits until every Prompt and Answer the model started has
// returned, or timeout, and reports whether they all did. A host calls it
// after the program ends and the run was aborted, before it closes the
// store the run is writing its end to. Call it only once the program has
// stopped sending messages to the model.
func (m *Model) Drain(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() { m.inflight.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (m *Model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.running() && !m.aborting {
			m.aborting = true
			m.ctl.Abort()
			m.relayout()
			return m, nil
		}
		// Idle, or an abort that has not ended the run: leave.
		return m, tea.Quit
	case "pgup":
		m.vp.PageUp()
		return m, nil
	case "pgdown":
		m.vp.PageDown()
		return m, nil
	case "up":
		m.vp.ScrollUp(1)
		return m, nil
	case "down":
		m.vp.ScrollDown(1)
		return m, nil
	case "home":
		m.vp.GotoTop()
		return m, nil
	case "end":
		m.vp.GotoBottom()
		return m, nil
	case "ctrl+r":
		m.o.reasoning = !m.o.reasoning
		m.relayout()
		return m, nil
	case "ctrl+o":
		m.o.output = !m.o.output
		m.relayout()
		return m, nil
	}
	if p, _, ok := m.pending(); ok {
		return m.permissionKey(msg, p)
	}
	if msg.Type == tea.KeyEnter {
		return m.send()
	}
	var cmd tea.Cmd
	m.in, cmd = m.in.Update(msg)
	return m, cmd
}

// permissionKey handles a key while a permission is asked: y approves, n
// starts a refusal whose optional reason is typed in the input, and Enter
// then refuses with it.
func (m *Model) permissionKey(msg tea.KeyMsg, p view.Permission) (tea.Model, tea.Cmd) {
	if m.refusing {
		switch msg.Type {
		case tea.KeyEnter:
			reason := strings.TrimSpace(m.in.Value())
			m.in.Reset()
			m.refusing = false
			return m.decide(refusal(p, reason))
		case tea.KeyEsc:
			m.in.Reset()
			m.refusing = false
			m.relayout()
			return m, nil
		}
		var cmd tea.Cmd
		m.in, cmd = m.in.Update(msg)
		return m, cmd
	}
	switch msg.String() {
	case "y", "Y":
		return m.decide(agentturn.Approve(p.CallID).WithBy(who))
	case "n", "N":
		m.refusing = true
		m.in.Reset()
		m.relayout()
	}
	return m, nil
}

func refusal(p view.Permission, reason string) agentturn.Answer {
	text := "The user refused this call."
	if reason != "" {
		text += " Reason: " + reason
	}
	a := agentturn.Refuse(openresponses.NewFunctionCallOutput(p.CallID, text)).WithBy(who)
	if reason != "" {
		a = a.WithReason(reason)
	}
	return a
}

// decide records the answer to the permission asked, and once every
// permission out has one, sends them all.
func (m *Model) decide(a agentturn.Answer) (tea.Model, tea.Cmd) {
	m.decided[a.CallID] = a
	if _, _, more := m.pending(); more {
		m.relayout()
		return m, nil
	}
	var answers []agentturn.Answer
	for _, p := range m.view.Permissions {
		answers = append(answers, m.decided[p.CallID])
	}
	m.busy = true
	m.err = ""
	m.relayout()
	ctl, ctx := m.ctl, m.ctx
	m.inflight.Add(1)
	return m, func() tea.Msg {
		defer m.inflight.Done()
		return runDoneMsg{err: ctl.Answer(ctx, answers...)}
	}
}

// send takes the input: a prompt when idle, a steer while a run goes.
func (m *Model) send() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.in.Value())
	if text == "" {
		return m, nil
	}
	m.in.Reset()
	item := openresponses.UserText(text)
	if m.running() {
		m.ctl.Steer(item)
		return m, nil
	}
	m.busy = true
	m.err = ""
	m.relayout()
	ctl, ctx := m.ctl, m.ctx
	m.inflight.Add(1)
	return m, func() tea.Msg {
		defer m.inflight.Done()
		return runDoneMsg{err: ctl.Prompt(ctx, item)}
	}
}

// relayout sizes the parts and refills the viewport, keeping it at the
// bottom if it was there.
func (m *Model) relayout() {
	if !m.ready {
		return
	}
	atBottom := m.vp.AtBottom()
	panel := m.panel()
	used := 2 // status line and input
	if panel != "" {
		used += lipgloss.Height(panel)
	}
	if m.err != "" {
		used++
	}
	m.vp.Width = m.width
	m.vp.Height = max(m.height-used, 1)
	m.vp.SetContent(renderRows(m.view, m.o, m.width))
	if atBottom {
		m.vp.GotoBottom()
	}
	if _, _, ok := m.pending(); ok && !m.refusing {
		m.in.Blur()
	} else {
		m.in.Focus()
	}
}

// panel is the permission being asked about, if one is.
func (m *Model) panel() string {
	p, i, ok := m.pending()
	if !ok {
		return ""
	}
	var b strings.Builder
	b.WriteString(warnStyle.Render("Permission requested") + " (" + itoa(i+1) + "/" + itoa(len(m.view.Permissions)) + "): " + p.Name)
	if p.Args != "" {
		b.WriteString(" " + clipLine(p.Args, false))
	}
	if p.Question != "" {
		b.WriteString("\n  " + p.Question)
	}
	if m.refusing {
		b.WriteString("\n  Reason for refusing (optional), Enter to refuse, Esc to go back")
	} else {
		b.WriteString("\n  [y] approve   [n] refuse")
	}
	return wrap(b.String(), max(m.width, 10))
}

// feedNote prefixes the status line when a stream stopped.
func feedNote(feedErr string) string {
	if feedErr == "" {
		return ""
	}
	return "FEED STOPPED (" + feedErr + "), quit and resume the session | "
}

func itoa(n int) string { return strconv.Itoa(n) }

// View implements tea.Model.
func (m *Model) View() string {
	if !m.ready {
		return "starting..."
	}
	parts := []string{
		statusStyle.Render(padTo(feedNote(m.feedErr)+statusText(m.view, m.busy, m.aborting, m.verified), m.width)),
		m.vp.View(),
	}
	if p := m.panel(); p != "" {
		parts = append(parts, p)
	}
	if m.err != "" {
		parts = append(parts, errStyle.Render(truncate(m.err, m.width)))
	}
	parts = append(parts, m.in.View())
	return strings.Join(parts, "\n")
}

func padTo(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return truncate(s, w)
}

func truncate(s string, w int) string {
	if w <= 0 || lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r)) > w {
		r = r[:len(r)-1]
	}
	return string(r)
}
