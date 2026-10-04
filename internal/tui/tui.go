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

	"github.com/ChristopherDavenport/agentconsole/client"
	"github.com/ChristopherDavenport/agentconsole/internal/inspect"
	"github.com/ChristopherDavenport/agentconsole/view"
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
	// flips are the rows shown the other way round from o, by entry: what
	// ctrl+r and ctrl+o toggled with the row selected. Toggling a switch
	// for every row drops the rows' own flips of it.
	flips map[string]opts
	// spans are where the rows sit in the viewport's content, from the
	// last layout, for a click to find the row under it.
	spans []rowSpan

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

	rec  client.Record
	insp *inspect.Inspector

	// screen is the conversation or the tree; pane the detail under the
	// conversation; frozen a read-only look at another line, nil when the
	// live view is shown (see nav.go).
	screen screen
	pane   pane
	frozen *frozen
	// curEntry is the row the cursor rests on, by its entry; reveal asks
	// the next layout to scroll it into view.
	curEntry string
	reveal   bool
	panes    paneState
	treeCur  int
	// drawn is the screen the viewport's content was last laid out for.
	drawn screen
	// note is the last thing a client action did, shown on the status line
	// until the next one.
	note string
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
		rec:      be.Record(),
		insp:     inspect.New(be.Record()),
		vp:       viewport.New(0, 0),
		in:       in,
		flips:    map[string]opts{},
		decided:  map[string]agentturn.Answer{},
	}
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd { return textinput.Blink }

func (m *Model) running() bool { return m.busy || m.view.Turn.State == view.Running }

// pending is the permission being asked about: the first of the ones out
// that has no answer yet.
func (m *Model) pending() (view.Permission, int, bool) {
	if m.running() || m.frozen != nil || m.screen != screenConversation {
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
		m.keepCursor()
		m.relayout()
		return m, m.wantPane()
	case paneMsg:
		if msg.seq == m.panes.seq && msg.key == m.panes.want {
			m.panes.have, m.panes.lines, m.panes.err = msg.key, msg.lines, ""
			if msg.err != nil {
				m.panes.err = msg.err.Error()
			}
			m.relayout()
		}
		return m, nil
	case openMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
		} else {
			m.frozen, m.err, m.curEntry = msg.frozen, "", msg.frozen.selected
			m.panes = paneState{}
			m.reveal = m.curEntry != ""
		}
		m.relayout()
		return m, m.wantPane()
	case continueMsg:
		m.busy = false
		m.err = ""
		if msg.err != nil {
			m.err = "continue from here: " + msg.err.Error()
		} else {
			m.frozen, m.curEntry, m.note = nil, "", ""
			m.noteHead(msg.entry)
			m.screen = screenConversation
		}
		m.in.Focus()
		m.relayout()
		return m, m.wantPane()
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
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			return m, m.click(msg.Y)
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	case InterruptMsg:
		return m.interrupt()
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

// InterruptMsg is an interrupt from outside the terminal, SIGINT or
// SIGTERM: it does what Ctrl-C does. The host turns the program's own
// signal handling off (tea.WithoutSignalHandler) and sends this instead,
// since bubbletea's ends the program with an error and no abort.
type InterruptMsg struct{}

// interrupt aborts a running run, and quits when idle or when an abort
// has not ended the run.
func (m *Model) interrupt() (tea.Model, tea.Cmd) {
	if m.running() && !m.aborting {
		m.aborting = true
		m.ctl.Abort()
		m.relayout()
		return m, nil
	}
	return m, tea.Quit
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
		return m.interrupt()
	case "ctrl+t":
		if m.screen == screenTree {
			m.screen = screenConversation
		} else {
			m.screen, m.treeCur = screenTree, 0
			for i, it := range m.treeItems() {
				if it.branch && it.leaf == m.view.Leaf {
					m.treeCur = i
				}
			}
		}
		m.relayout()
		return m, nil
	case "pgup":
		m.vp.PageUp()
		return m, nil
	case "pgdown":
		m.vp.PageDown()
		return m, nil
	case "ctrl+up":
		m.vp.ScrollUp(1)
		return m, nil
	case "ctrl+down":
		m.vp.ScrollDown(1)
		return m, nil
	case "ctrl+home":
		m.vp.GotoTop()
		return m, nil
	case "ctrl+end":
		m.vp.GotoBottom()
		return m, nil
	case "ctrl+r":
		m.toggle(opts{reasoning: true})
		return m, nil
	case "ctrl+o":
		m.toggle(opts{output: true})
		return m, nil
	}
	if m.screen == screenTree {
		return m.treeKey(msg)
	}
	switch msg.String() {
	case "tab":
		return m, m.cyclePane()
	case "ctrl+p":
		return m, m.moveCursor(-1)
	case "ctrl+n":
		return m, m.moveCursor(1)
	case "ctrl+b":
		return m, m.continueFrom(m.curEntry)
	}
	if m.frozen != nil {
		switch msg.String() {
		case "esc":
			m.frozen, m.curEntry, m.panes = nil, "", paneState{}
			m.relayout()
			return m, m.wantPane()
		case "c":
			if m.curEntry != "" {
				return m, m.continueFrom(m.curEntry)
			}
			return m, m.continueFrom(m.frozen.model.Leaf)
		}
		return m, nil // read only: the input is off
	}
	if msg.String() == "esc" && (m.curEntry != "" || m.pane != paneNone) {
		m.curEntry, m.pane, m.panes = "", paneNone, paneState{}
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
	atBottom := m.vp.AtBottom() || m.drawn != m.screen
	panel := m.panel()
	pane := m.paneLines()
	used := 2 // status line and input
	if panel != "" {
		used += lipgloss.Height(panel)
	}
	if m.screen == screenConversation {
		used += len(pane)
	}
	if m.err != "" {
		used++
	}
	m.vp.Width = m.width
	m.vp.Height = max(m.height-used, 1)
	if m.screen == screenTree {
		content, line := m.renderTree()
		m.vp.SetContent(content)
		m.reveal = false
		m.showLine(line, 1)
		m.drawn = m.screen
		return
	}
	content, spans := renderRows(m.shown(), m.o, m.flips, m.width, m.curEntry)
	m.vp.SetContent(content)
	m.spans = spans
	switch {
	case m.reveal && m.curEntry != "":
		for _, sp := range spans {
			if sp.entry == m.curEntry {
				m.showLine(sp.start, sp.height)
			}
		}
	case atBottom:
		m.vp.GotoBottom()
	}
	m.reveal = false
	m.drawn = m.screen
	if _, _, ok := m.pending(); ok && !m.refusing {
		m.in.Blur()
	} else {
		m.in.Focus()
	}
}

// showLine scrolls the viewport so lines line..line+height-1 are visible,
// as far as the viewport is tall enough to show them.
func (m *Model) showLine(line, height int) {
	switch {
	case line < m.vp.YOffset:
		m.vp.SetYOffset(line)
	case line+height > m.vp.YOffset+m.vp.Height:
		m.vp.SetYOffset(min(line, line+height-m.vp.Height))
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
	status := feedNote(m.feedErr) + statusText(m.view, m.busy, m.aborting, m.verified)
	if m.note != "" {
		status += " | " + m.note
	}
	if b := m.frozenBanner(); b != "" {
		status = b + " | " + status
	}
	parts := []string{statusStyle.Render(padTo(status, m.width)), m.vp.View()}
	if m.screen == screenConversation {
		parts = append(parts, m.paneLines()...)
	}
	if p := m.panel(); p != "" {
		parts = append(parts, p)
	}
	if m.err != "" {
		parts = append(parts, errStyle.Render(truncate(m.err, m.width)))
	}
	switch {
	case m.screen == screenTree:
		parts = append(parts, dimStyle.Render(truncate("tree: up/down select, enter view the branch, c continue from here, esc back", m.width)))
	case m.frozen != nil:
		parts = append(parts, dimStyle.Render(truncate("read only: esc back to the live session, c continue from here, tab detail", m.width)))
	default:
		parts = append(parts, m.in.View())
	}
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
