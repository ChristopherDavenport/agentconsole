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
	"unicode"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"

	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client"
	"github.com/ChristopherDavenport/agentconsole/internal/inspect"
	"github.com/ChristopherDavenport/agentconsole/view"
)

// who is what the record says answered a permission.
const who = "human"

// inputMaxRows is how tall the prompt grows before it scrolls: a wrap
// with room to read what is typed, instead of one line that scrolls
// sideways.
const inputMaxRows = 5

// runDoneMsg says a Prompt or Answer returned: the run ended, or never
// started.
type runDoneMsg struct{ err error }

// replyDoneMsg is a reply to a question sent, or the error sending it.
type replyDoneMsg struct {
	id  string
	err error
}

// Model is the Bubble Tea model.
type Model struct {
	ctx      context.Context
	ctl      client.Control
	verified bool

	view view.Model // the latest the feed sent
	vp   viewport.Model
	in   textarea.Model
	// spinner turns on the run line and the calls in motion while a run
	// goes; spinning is whether its ticks are coming, so a second chain
	// of them is never started.
	spinner  spinner.Model
	spinning bool
	o        opts
	// md renders the assistant's markdown, and keeps what it rendered
	// for the next layout.
	md *markdown
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
	// replied are the questions answered whose close the view has not
	// shown yet, by ID; refusingQ is set while a question's refusal
	// reason is typed.
	replied   map[string]bool
	refusingQ bool

	rec  client.Record
	insp *inspect.Inspector
	// cost prices one model call for the session pane; nil shows no
	// cost.
	cost client.Cost

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
	// The mouse selection (select.go): pressed is whether the left
	// button is down, pressX and pressY where it went down, sel the region
	// being dragged over, lines the frame's lines and content the
	// viewport's content's, for the copy to read. copier is where a copy
	// goes; nil draws but never copies.
	pressed        bool
	pressX, pressY int
	sel            *selection
	lines          []string
	content        []string
	copier         func(string)
	// treeLines is the content line of each item of the tree, for a click.
	treeLines []int
	// inputY is the screen line the input's first row was last drawn on,
	// -1 when it was not drawn.
	inputY int
	// drawn is the screen the viewport's content was last laid out for.
	drawn screen
	// note is the last thing a client action did, shown on the status line
	// until the next one.
	note string
}

var _ tea.Model = (*Model)(nil)

// Option configures [New].
type Option func(*Model)

// WithCost sets the function that prices one model call, in US dollars;
// it feeds the session pane's total. Without it the pane shows tokens
// but no cost.
func WithCost(fn client.Cost) Option { return func(m *Model) { m.cost = fn } }

// New returns the model for a backend. The caller starts [Attach] with
// the program's Send; ctx ends the runs the model starts.
func New(ctx context.Context, be client.Backend, options ...Option) *Model {
	in := textarea.New()
	in.Prompt = "> "
	in.Placeholder = "say something (ctrl+/ for keys)"
	in.ShowLineNumbers = false
	in.MaxHeight = inputMaxRows
	in.KeyMap.InsertNewline.SetEnabled(false)
	in.CharLimit = 0
	in.SetStyles(inputStyles(true))
	in.Focus()
	m := &Model{
		ctx:      ctx,
		ctl:      be.Control(),
		verified: be.Record().Verified(),
		rec:      be.Record(),
		vp:       viewport.New(),
		in:       in,
		spinner:  spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		md:       newMarkdown(),
		flips:    map[string]opts{},
		decided:  map[string]agentturn.Answer{},
		replied:  map[string]bool{},
		inputY:   -1,
	}
	for _, o := range options {
		o(m)
	}
	inspOpts := []inspect.Option{}
	if m.cost != nil {
		inspOpts = append(inspOpts, inspect.WithCost(m.cost))
	}
	m.insp = inspect.New(be.Record(), inspOpts...)
	return m
}

// inputStyles are the textarea's default styles for a dark or a light
// terminal, less the background they give the line the cursor is on: the
// input is drawn on the terminal's own background, as the rest of the
// screen is.
func inputStyles(isDark bool) textarea.Styles {
	s := textarea.DefaultStyles(isDark)
	s.Focused.CursorLine = s.Focused.CursorLine.UnsetBackground()
	return s
}

// Init implements tea.Model. The textarea's styles and the markdown's
// (glamour's dark or light) depend on whether the terminal's background
// is dark, which the terminal is asked for.
func (m *Model) Init() tea.Cmd { return tea.Batch(textarea.Blink, tea.RequestBackgroundColor) }

func (m *Model) running() bool { return m.busy || m.view.Turn.State == view.Running }

// spin starts the spinner's ticks when a run goes and they are not
// coming already. They stop at the first tick after the run.
func (m *Model) spin() tea.Cmd {
	if m.spinning || !m.running() {
		return nil
	}
	m.spinning = true
	return m.spinner.Tick
}

// question is the question being asked: the first a running call waits
// on that has no reply yet. It is asked while the run goes, on the
// conversation, ahead of any permission.
func (m *Model) question() (client.Question, int, bool) {
	if m.frozen != nil || m.screen != screenConversation {
		return client.Question{}, 0, false
	}
	for i, q := range m.view.Questions {
		if !m.replied[q.ID] {
			return q, i, true
		}
	}
	return client.Question{}, 0, false
}

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
		// A program whose output is not a terminal and was given no size
		// reports 0x0 at its start: wait for a size to lay out in.
		if msg.Width <= 0 || msg.Height <= 0 {
			return m, nil
		}
		m.width, m.height, m.ready = msg.Width, msg.Height, true
		m.in.SetWidth(max(msg.Width-4, 1))
		m.relayout()
		return m, nil
	case ModelMsg:
		m.view = msg.Model
		m.syncPermissions()
		m.syncQuestions()
		m.keepCursor()
		m.relayout()
		return m, tea.Batch(m.wantPane(), m.spin())
	case spinner.TickMsg:
		if !m.running() {
			m.spinning = false
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		// The run line reads the spinner as it is drawn; the calls in
		// motion are in the viewport's content, laid out again for them.
		if anyMoving(m.shown()) {
			m.relayout()
		}
		return m, cmd
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
	case replyDoneMsg:
		if msg.err != nil {
			delete(m.replied, msg.id)
			m.err = "reply: " + msg.err.Error()
		}
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
		// The run's end unpins the session pane, which waits for it;
		// ask for its data now, since the final ModelMsg may have been
		// handled while the run's Prompt was still returning, with the
		// pane still pinned.
		return m, m.wantPane()
	case tea.BackgroundColorMsg:
		m.in.SetStyles(inputStyles(msg.IsDark()))
		m.md.setDark(msg.IsDark())
		m.relayout()
		return m, nil
	case tea.MouseMsg:
		return m, m.mouse(msg)
	case InterruptMsg:
		return m.interrupt()
	case tea.PasteMsg:
		m.clearSelection()
		return m, m.paste(msg)
	case tea.KeyPressMsg:
		// Ctrl+c with a selection drawn copies it, as a desktop's copy
		// does, and drops it, so the next ctrl+c is the interrupt again.
		// Any other key drops the selection.
		if m.sel != nil && msg.String() == "ctrl+c" {
			m.copySelection()
			m.clearSelection()
			return m, nil
		}
		m.clearSelection()
		return m.key(msg)
	}
	return m, m.updateInput(msg)
}

// syncQuestions forgets replies to questions the view no longer lists,
// and leaves a refusal being typed for one that closed without it.
func (m *Model) syncQuestions() {
	open := map[string]bool{}
	for _, q := range m.view.Questions {
		open[q.ID] = true
	}
	for id := range m.replied {
		if !open[id] {
			delete(m.replied, id)
		}
	}
	if _, _, ok := m.question(); !ok && m.refusingQ {
		m.refusingQ = false
		m.in.Reset()
	}
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
// SIGTERM: it does what Ctrl-C does with nothing selected — a signal
// always interrupts, never copies. The host turns the program's own
// signal handling off (tea.WithoutSignalHandler) and sends this
// instead, since bubbletea's ends the program with an error and no
// abort.
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

func (m *Model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if isKeysKey(msg) {
		if m.screen == screenKeys {
			m.screen = screenConversation
		} else {
			m.screen = screenKeys
		}
		m.relayout()
		return m, nil
	}
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
	switch m.screen {
	case screenTree:
		return m.treeKey(msg)
	case screenKeys:
		return m.keysKey(msg)
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
	if q, _, ok := m.question(); ok {
		return m.questionKey(msg, q)
	}
	if p, _, ok := m.pending(); ok {
		return m.permissionKey(msg, p)
	}
	if isNewlineKey(msg) {
		return m, m.newline()
	}
	if msg.Code == tea.KeyEnter {
		return m.send()
	}
	return m, m.updateInput(msg)
}

// newline breaks the input's line at the cursor, as a pasted line break
// does: through the paste path, which replaces a selection and is not
// held to the textarea's MaxHeight lines, as its own newline key is.
func (m *Model) newline() tea.Cmd { return m.updateInput(tea.PasteMsg{Content: "\n"}) }

// paste puts a paste in the input where a typed key would reach it: on
// the conversation and not read only. A paste is never a y or an n: while
// a question or a permission waits for one the input is blurred and takes
// nothing, until a refusal's reason is being typed.
func (m *Model) paste(msg tea.PasteMsg) tea.Cmd {
	if !m.typing() {
		return nil
	}
	return m.updateInput(msg)
}

// permissionKey handles a key while a permission is asked: y approves, n
// starts a refusal whose optional reason is typed in the input, and Enter
// then refuses with it.
func (m *Model) permissionKey(msg tea.KeyPressMsg, p view.Permission) (tea.Model, tea.Cmd) {
	if m.refusing {
		if isNewlineKey(msg) {
			return m, m.newline()
		}
		switch msg.Code {
		case tea.KeyEnter:
			reason := strings.TrimSpace(m.in.Value())
			m.in.Reset()
			m.refusing = false
			return m.decide(refusal(p, reason))
		case tea.KeyEscape:
			m.in.Reset()
			m.refusing = false
			m.relayout()
			return m, nil
		}
		return m, m.updateInput(msg)
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

// questionKey handles a key while a question is asked, as permissionKey
// does a permission's: y allows, n starts a refusal whose optional
// reason is typed, Enter sends it and Esc goes back.
func (m *Model) questionKey(msg tea.KeyPressMsg, q client.Question) (tea.Model, tea.Cmd) {
	if m.refusingQ {
		if isNewlineKey(msg) {
			return m, m.newline()
		}
		switch msg.Code {
		case tea.KeyEnter:
			note := strings.TrimSpace(m.in.Value())
			m.in.Reset()
			m.refusingQ = false
			return m.reply(q, client.Reply{Note: note})
		case tea.KeyEscape:
			m.in.Reset()
			m.refusingQ = false
			m.relayout()
			return m, nil
		}
		return m, m.updateInput(msg)
	}
	switch msg.String() {
	case "y", "Y":
		return m.reply(q, client.Reply{Accept: true})
	case "n", "N":
		m.refusingQ = true
		m.in.Reset()
		m.relayout()
	}
	return m, nil
}

// reply sends the answer to q. The call that asked goes on at once, so
// nothing waits on it here; the panel moves to the next question.
func (m *Model) reply(q client.Question, r client.Reply) (tea.Model, tea.Cmd) {
	m.replied[q.ID] = true
	m.err = ""
	m.relayout()
	ctl := m.ctl
	return m, func() tea.Msg { return replyDoneMsg{id: q.ID, err: ctl.Reply(q.ID, r)} }
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
	return m, tea.Batch(func() tea.Msg {
		defer m.inflight.Done()
		return runDoneMsg{err: ctl.Answer(ctx, answers...)}
	}, m.spin())
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
	return m, tea.Batch(func() tea.Msg {
		defer m.inflight.Done()
		return runDoneMsg{err: ctl.Prompt(ctx, item)}
	}, m.spin())
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
	rows := inputRowsUsed(m.in.Value(), m.in.Width())
	visible := min(rows, inputMaxRows)
	m.in.SetHeight(visible)
	used := 4 // the status line, the run line and the blank over it, the hint the input's place takes
	if m.typing() {
		used = 1 + 2 + 1 + visible + 2 // status, the run line and the blank over it, the buffer, the input's rows, its bars
	}
	if panel != "" {
		used += lipgloss.Height(panel)
	}
	if m.screen == screenConversation {
		used += len(pane)
	}
	if m.err != "" {
		used++
	}
	m.vp.SetWidth(m.width)
	m.vp.SetHeight(max(m.height-used, 1))
	if m.screen == screenKeys {
		m.setContent(wrap(renderKeys(), max(m.width, 10)))
		if m.drawn != m.screen {
			m.vp.GotoTop()
		}
		m.drawn = m.screen
		return
	}
	if m.screen == screenTree {
		content, line, lines := m.renderTree()
		m.setContent(content)
		m.treeLines = lines
		m.reveal = false
		m.showLine(line, 1)
		m.drawn = m.screen
		return
	}
	content, spans := renderRows(m.shown(), m.o, m.flips, m.width, m.curEntry, m.spinner.View(), m.md)
	m.setContent(content)
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
	_, _, asking := m.question()
	_, _, permitting := m.pending()
	if (asking && !m.refusingQ) || (!asking && permitting && !m.refusing) {
		m.in.Blur()
	} else {
		m.in.Focus()
	}
}

// setContent fills the viewport and keeps its lines, for a selection on
// them to copy.
func (m *Model) setContent(s string) {
	m.vp.SetContent(s)
	m.content = strings.Split(s, "\n")
}

// showLine scrolls the viewport so lines line..line+height-1 are visible,
// as far as the viewport is tall enough to show them.
func (m *Model) showLine(line, height int) {
	switch {
	case line < m.vp.YOffset():
		m.vp.SetYOffset(line)
	case line+height > m.vp.YOffset()+m.vp.Height():
		m.vp.SetYOffset(min(line, line+height-m.vp.Height()))
	}
}

// panel is the question or the permission being asked about, if one is.
func (m *Model) panel() string {
	if q, i, ok := m.question(); ok {
		return m.questionPanel(q, i)
	}
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

// questionPanel is the panel for a question a running call asked.
func (m *Model) questionPanel(q client.Question, i int) string {
	var b strings.Builder
	b.WriteString(warnStyle.Render("Question") + " (" + itoa(i+1) + "/" + itoa(len(m.view.Questions)) + ")")
	if q.Call != nil {
		b.WriteString(": " + q.Call.Name)
		if q.Call.Arguments != "" {
			b.WriteString(" " + clipLine(q.Call.Arguments, false))
		}
	}
	if q.Text != "" {
		b.WriteString("\n  " + q.Text)
	}
	switch {
	case m.refusingQ:
		b.WriteString("\n  Reason for refusing (optional), Enter to refuse, Esc to go back")
	case q.Call != nil:
		b.WriteString("\n  [y] allow   [n] refuse")
	default:
		b.WriteString("\n  [y] yes   [n] no")
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

// View implements tea.Model. The program takes the whole screen and
// reports the mouse, motion with a button down included, for selecting.
func (m *Model) View() tea.View {
	v := tea.NewView(m.frame())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// frame is the screen's content.
func (m *Model) frame() string {
	if !m.ready {
		return "starting..."
	}
	state, now := turnState(m.view, m.busy, m.aborting), time.Now()
	status := feedNote(m.feedErr) + statusText(m.view, sessionTime(m.view, state, now), m.verified, m.cost)
	if m.note != "" {
		status += " | " + m.note
	}
	if b := m.frozenBanner(); b != "" {
		status = b + " | " + status
	}
	parts := []string{statusStyle.Render(padTo(status, m.width)), m.vp.View()}
	inputY := -1
	if m.screen == screenConversation {
		parts = append(parts, m.paneLines()...)
	}
	if p := m.panel(); p != "" {
		parts = append(parts, p)
	}
	if m.err != "" {
		parts = append(parts, errStyle.Render(truncate(m.err, m.width)))
	}
	// A blank line keeps the run line off what is above it.
	parts = append(parts, "", runLine(state, runFigures(m.view, state, now), m.spinner.View(), m.width))
	switch {
	case m.screen == screenTree:
		parts = append(parts, dimStyle.Render(truncate("tree: up/down select, enter view the branch, c continue from here, esc back", m.width)))
	case m.screen == screenKeys:
		parts = append(parts, dimStyle.Render(truncate("keys: esc, q or ctrl+/ back; pgup/pgdn scroll", m.width)))
	case m.frozen != nil:
		parts = append(parts, dimStyle.Render(truncate("read only: esc back to the live session, c continue from here, tab detail", m.width)))
	default:
		bar := dimStyle.Render(strings.Repeat("─", max(m.width, 1)))
		// A blank line keeps the input off the run line. The input
		// starts under the bar, below every line so far.
		parts = append(parts, "")
		inputY = strings.Count(strings.Join(parts, "\n"), "\n") + 2
		parts = append(parts, bar, m.in.View(), bar)
	}
	m.inputY = inputY
	s := strings.Join(parts, "\n")
	// The frame's lines, for a selection's copy to read; the selection is
	// drawn over them without changing the text.
	m.lines = strings.Split(s, "\n")
	if m.sel != nil {
		s = strings.Join(m.markSelection(m.lines), "\n")
	}
	return s
}

// typing is whether the input line is shown, between its bars: not on the
// tree or the keys, nor on a read-only view, whose last line is a hint
// instead.
func (m *Model) typing() bool {
	return m.screen == screenConversation && m.frozen == nil
}

// updateInput edits the input and then lays the screen out again. The
// textarea is given its full height first, so its own scroll follows the
// cursor while the key is applied; relayout shrinks the box to what the
// value actually needs.
func (m *Model) updateInput(msg tea.Msg) tea.Cmd {
	m.in.SetHeight(inputMaxRows)
	var cmd tea.Cmd
	m.in, cmd = m.in.Update(msg)
	m.relayout()
	return cmd
}

// inputRowsUsed is how many screen rows the input needs for value at
// width columns: each logical line contributes its soft-wrapped rows. A
// pasted value keeps its newlines, so blank input is one row and a
// four-line paste is four rows (more when a line wraps). A value ending
// in a newline ends on an empty row, where the cursor sits after a line
// break, so the lines are split on the newline, as the textarea keeps
// them, and not taken by strings.Lines, which has no row after the last.
func inputRowsUsed(value string, width int) int {
	rows := 0
	for line := range strings.SplitSeq(value, "\n") {
		rows += wrapRows([]rune(line), width)
	}
	return max(rows, 1)
}

// wrapRows counts the rows wrap admits for one logical line.
func wrapRows(runes []rune, width int) int { return len(wrapLine(runes, width)) }

// wrapLine is the rows one logical line soft-wraps into, with the same
// algorithm the textarea applies. It is a copy of the unexported wrap
// function in charm.land/bubbles/v2@v2.2.1's textarea package (the same
// as bubbles v1.0.0's); bubbles exposes nothing better, so keep this in
// step when bubbles is upgraded.
func wrapLine(runes []rune, width int) [][]rune {
	var (
		lines  = [][]rune{{}}
		word   []rune
		row    int
		spaces int
	)
	for _, r := range runes {
		if unicode.IsSpace(r) {
			spaces++
		} else {
			word = append(word, r)
		}

		if spaces > 0 {
			if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces > width {
				row++
				lines = append(lines, []rune{})
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], repeatSpaces(spaces)...)
				spaces = 0
				word = nil
			} else {
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], repeatSpaces(spaces)...)
				spaces = 0
				word = nil
			}
		} else {
			lastCharLen := runewidth.RuneWidth(word[len(word)-1])
			if uniseg.StringWidth(string(word))+lastCharLen > width {
				if len(lines[row]) > 0 {
					row++
					lines = append(lines, []rune{})
				}
				lines[row] = append(lines[row], word...)
				word = nil
			}
		}
	}

	if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces >= width {
		lines = append(lines, []rune{})
		lines[row+1] = append(lines[row+1], word...)
		spaces++
		lines[row+1] = append(lines[row+1], repeatSpaces(spaces)...)
	} else {
		lines[row] = append(lines[row], word...)
		spaces++
		lines[row] = append(lines[row], repeatSpaces(spaces)...)
	}

	return lines
}

func repeatSpaces(n int) []rune { return []rune(strings.Repeat(" ", n)) }

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
