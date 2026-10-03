package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ChristopherDavenport/agentsession"

	"github.com/ChristopherDavenport/agentconsole/internal/inspect"
	"github.com/ChristopherDavenport/agentconsole/internal/view"
)

// The viewing state: which screen, which pane under the conversation, and
// whether what is shown is the live session or a read-only look at
// another branch or session.
//
// # Viewing is local, continuing moves the head
//
// Opening a branch in the tree changes only what this client shows: it
// reads the session, renders the line ending at the branch's tip
// ([view.At]) and holds that as a frozen view beside the live one. The
// agent's head does not move, the feed keeps running, and Esc returns to
// the live view. The head moves on an explicit action, "continue from
// here" (c on a branch in the tree or a frozen view, ctrl+b on the
// selected row), which calls [client.Control.ContinueFrom]; the next
// prompt then continues from that entry, and the live view follows the
// head, since the follower reports it.

type screen int

const (
	screenConversation screen = iota
	screenTree
)

type pane int

const (
	paneNone pane = iota
	paneDetail
	paneSession
)

// frozen is a read-only view of a line the agent is not on: another
// branch of the session, or another session (the origin of a fork, a
// subsession, a successor).
type frozen struct {
	title   string
	session string
	model   view.Model
	// other is set when it is not the followed session.
	other bool
	// select is the row the view opens with the cursor on: the entry a
	// fork was made at, in its origin.
	selected string
}

// shown is the model the conversation renders: the frozen view, or the
// live one.
func (m *Model) shown() view.Model {
	if m.frozen != nil {
		return m.frozen.model
	}
	return m.view
}

// selectable are the entries of the shown rows a cursor can rest on, in
// order: the committed rows that render.
func selectable(m view.Model) []string {
	hidden := hiddenOutputs(m)
	var out []string
	for i, row := range m.Rows {
		if row.EntryID == "" || hidden[i] {
			continue
		}
		out = append(out, row.EntryID)
	}
	return out
}

// moveCursor moves the row cursor by delta, starting from the last row
// when it is not set, and opens the detail pane's data for it.
func (m *Model) moveCursor(delta int) tea.Cmd {
	ids := selectable(m.shown())
	if len(ids) == 0 {
		return nil
	}
	at := len(ids) // none: one past the end, so up lands on the last
	if m.curEntry != "" {
		for i, id := range ids {
			if id == m.curEntry {
				at = i
			}
		}
	} else if delta > 0 {
		return nil
	}
	at = min(max(at+delta, 0), len(ids)-1)
	m.curEntry = ids[at]
	m.reveal = true
	m.relayout()
	return m.wantPane()
}

// keepCursor drops a cursor whose row is gone from the shown line.
func (m *Model) keepCursor() {
	if m.curEntry == "" {
		return
	}
	for _, id := range selectable(m.shown()) {
		if id == m.curEntry {
			return
		}
	}
	m.curEntry = ""
	if m.pane == paneDetail {
		m.pane = paneNone
	}
}

// paneState is what the pane under the conversation holds.
type paneState struct {
	// want is the key of what the pane should show; have the key of what
	// lines is. A pane whose have differs is loading.
	want, have string
	lines      []string
	err        string
	seq        int
}

// paneMsg is a computed pane.
type paneMsg struct {
	key   string
	seq   int
	lines []string
	err   error
}

// paneKey identifies what a pane shows. The detail of an entry is
// recomputed when the entry changes, or while the call it shows is still
// moving; the session pane when the line or the session's size does.
func (m *Model) paneKey() string {
	sh := m.shown()
	switch m.pane {
	case paneDetail:
		if m.curEntry == "" {
			return ""
		}
		key := sh.Session + "|" + sh.Tail + "|" + m.curEntry
		for _, row := range sh.Rows {
			if row.EntryID == m.curEntry && row.Call != nil && !row.Call.Committed {
				key += fmt.Sprintf("|%d", sh.Entries)
			}
		}
		return key
	case paneSession:
		// While a run goes the pane would be recomputed for every entry;
		// it waits for the run, and the model's own status shows progress.
		size := sh.Entries
		if m.frozen == nil && m.running() && m.panes.have != "" {
			size = -1
		}
		return fmt.Sprintf("session|%s|%s|%d", sh.Session, sh.Tail, size)
	}
	return ""
}

// wantPane asks for the pane's data if what it shows is not what it
// should: the command computes it off the program's goroutine.
func (m *Model) wantPane() tea.Cmd {
	key := m.paneKey()
	if key == "" || m.pane == paneNone {
		m.panes.want = ""
		return nil
	}
	if key == m.panes.want {
		return nil
	}
	if strings.HasSuffix(key, "|-1") {
		return nil // a run goes: keep what is shown
	}
	m.panes.want = key
	m.panes.seq++
	seq, sh, entry, pn, ctx, insp := m.panes.seq, m.shown(), m.curEntry, m.pane, m.ctx, m.insp
	return func() tea.Msg {
		var lines []string
		var err error
		switch pn {
		case paneDetail:
			var e inspect.Entry
			if e, err = insp.Entry(ctx, sh.Session, sh.Tail, entry, sh.Entries); err == nil {
				lines = e.Lines()
			}
		case paneSession:
			var s inspect.Session
			if s, err = insp.Session(ctx, sh.Session, sh.Tail, sh.Entries); err == nil {
				lines = s.Lines()
			}
		}
		return paneMsg{key: key, seq: seq, lines: lines, err: err}
	}
}

// cyclePane is Tab: no pane, the detail of the selected row, the session
// summary, and round again.
func (m *Model) cyclePane() tea.Cmd {
	switch m.pane {
	case paneNone:
		m.pane = paneDetail
		if m.curEntry == "" {
			if ids := selectable(m.shown()); len(ids) > 0 {
				m.curEntry = ids[len(ids)-1]
				m.reveal = true
			}
		}
	case paneDetail:
		m.pane = paneSession
	default:
		m.pane = paneNone
	}
	m.panes.want, m.panes.have, m.panes.lines, m.panes.err = "", "", nil, ""
	m.relayout()
	return m.wantPane()
}

// paneLines is what the pane shows now, clipped to the room it has.
func (m *Model) paneLines() []string {
	if m.pane == paneNone || !m.ready {
		return nil
	}
	title := "record detail"
	hint := "tab: session, ctrl+p/ctrl+n: row, esc: close"
	if m.pane == paneSession {
		title, hint = "session", "tab: close"
	}
	var body []string
	switch {
	case m.panes.err != "":
		body = []string{errStyle.Render(m.panes.err)}
	case m.pane == paneDetail && m.curEntry == "":
		body = []string{dimStyle.Render("no row selected: ctrl+p selects one")}
	case m.panes.have == "" || m.panes.have != m.panes.want:
		body = []string{dimStyle.Render("loading...")}
	default:
		body = m.panes.lines
	}
	room := max(m.height/2-2, 3)
	if len(body) > room {
		more := len(body) - (room - 1)
		body = append(append([]string(nil), body[:room-1]...), dimStyle.Render(fmt.Sprintf("... %d more lines", more)))
	}
	out := []string{dimStyle.Render("── " + title + " (" + hint + ") ──")}
	for _, l := range body {
		out = append(out, truncate(l, m.width))
	}
	return out
}

// openMsg is a read-only view that is ready.
type openMsg struct {
	frozen *frozen
	err    error
}

// open reads a session and shows the line ending at leaf, read-only. An
// empty leaf is the session's own head; mark, when the line holds it, is
// where the cursor starts, and when it does not the line is the one ending
// at mark, so a fork's origin opens on its own head with the fork point
// marked, or on the fork point when the origin has gone on elsewhere.
func (m *Model) open(title, sessionID, leaf, mark string) tea.Cmd {
	rec, ctx, live := m.rec, m.ctx, m.view.Session
	return func() tea.Msg {
		s, err := rec.Read(ctx, sessionID)
		if err != nil {
			return openMsg{err: fmt.Errorf("open %s: %w", title, err)}
		}
		mod := view.At(s, leaf)
		sel := ""
		if mark != "" {
			if slices.Contains(selectable(mod), mark) {
				sel = mark
			} else if _, ok := s.Entry(mark); ok {
				mod = view.At(s, mark)
				sel = mark
			}
		}
		return openMsg{frozen: &frozen{title: title, session: s.ID(), model: mod, other: s.ID() != live, selected: sel}}
	}
}

// continueMsg says ContinueFrom returned.
type continueMsg struct {
	entry string
	err   error
}

// continueFrom moves the agent's head to the entry.
func (m *Model) continueFrom(entry string) tea.Cmd {
	if entry == "" {
		return nil
	}
	if m.running() {
		m.err = "cannot move the head while a run goes"
		m.relayout()
		return nil
	}
	if m.frozen != nil && m.frozen.other {
		m.err = "this is another session: it can be read here, and resumed with --session"
		m.relayout()
		return nil
	}
	ctl, ctx := m.ctl, m.ctx
	m.busy = true // the head moves under the writer: no prompt meanwhile
	m.inflight.Add(1)
	return func() tea.Msg {
		defer m.inflight.Done()
		return continueMsg{entry: entry, err: ctl.ContinueFrom(ctx, entry)}
	}
}

// treeItem is one line of the tree screen.
type treeItem struct {
	text string
	// leaf is the branch tip, for a branch; session and at for a session
	// the line opens.
	leaf    string
	session string
	at      string
	title   string
	branch  bool
}

// treeItems lists what the tree shows, from the live view: the branches
// first, then where the session came from and the sessions it linked.
func (m *Model) treeItems() []treeItem {
	var out []treeItem
	for _, b := range m.view.Branches {
		when := ""
		if !b.Time.IsZero() {
			when = b.Time.Local().Format("15:04:05") + " "
		}
		mark := "  "
		if b.Current {
			mark = "* "
		}
		run := ""
		if b.RunID != "" {
			run = "  run " + shortID(b.RunID)
		}
		out = append(out, treeItem{
			text:   mark + when + b.Label + run,
			leaf:   b.Leaf,
			title:  "branch " + shortID(b.Leaf),
			branch: true,
		})
	}
	if o := m.view.Origin; o.ParentSession != "" {
		text := "  origin: session " + shortID(o.ParentSession)
		if o.Base != "" {
			text += " (forked at entry " + shortID(o.Base) + ")"
		} else {
			text += " (parent)"
		}
		out = append(out, treeItem{text: text, session: o.ParentSession, at: o.Base, title: "origin " + shortID(o.ParentSession)})
	}
	for _, l := range m.view.Links {
		var text string
		switch l.Rel {
		case agentsession.RelSubsession:
			text = "  subsession: " + shortID(l.Session)
			if l.CallID != "" {
				text += " (call " + l.CallID + ")"
			}
		case agentsession.RelContinuedIn:
			text = "  continued in: " + shortID(l.Session)
		case agentsession.RelForkOf:
			text = "  fork: " + shortID(l.Session)
		default:
			text = "  " + l.Rel + ": " + shortID(l.Session)
		}
		out = append(out, treeItem{text: text, session: l.Session, title: l.Rel + " " + shortID(l.Session)})
	}
	return out
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// renderTree renders the tree screen and the line the cursor is on.
func (m *Model) renderTree() (string, int) {
	items := m.treeItems()
	var b strings.Builder
	b.WriteString(dimStyle.Render("branches of session "+shortID(m.view.Session)+" (* is where the agent's head is)") + "\n")
	line := 0
	otherHeader := false
	rows := 1
	for i, it := range items {
		if !it.branch && !otherHeader {
			otherHeader = true
			b.WriteString("\n" + dimStyle.Render("other sessions") + "\n")
			rows += 2
		}
		cur := "  "
		if i == m.treeCur {
			cur = "▶ "
			line = rows
		}
		b.WriteString(cur + it.text + "\n")
		rows++
	}
	if len(items) == 0 {
		b.WriteString("  no branches yet\n")
	}
	return strings.TrimRight(b.String(), "\n"), line
}

// treeKey handles a key on the tree screen.
func (m *Model) treeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	items := m.treeItems()
	switch msg.String() {
	case "up", "k":
		m.treeCur = max(m.treeCur-1, 0)
	case "down", "j":
		m.treeCur = min(m.treeCur+1, max(len(items)-1, 0))
	case "esc", "ctrl+t", "q":
		m.screen = screenConversation
	case "enter":
		if m.treeCur >= len(items) {
			return m, nil
		}
		it := items[m.treeCur]
		m.screen = screenConversation
		if it.branch && it.leaf == m.view.Leaf {
			m.frozen = nil // the head's own line is the live view
			m.keepCursor()
			break
		}
		m.relayout()
		if it.branch {
			return m, m.open(it.title, m.view.Session, it.leaf, "")
		}
		return m, m.open(it.title, it.session, "", it.at)
	case "c":
		if m.treeCur >= len(items) || !items[m.treeCur].branch {
			return m, nil
		}
		return m, m.continueFrom(items[m.treeCur].leaf)
	}
	m.relayout()
	return m, nil
}

// frozenBanner is the status line's note on a read-only view.
func (m *Model) frozenBanner() string {
	if m.frozen == nil {
		return ""
	}
	return "VIEWING " + m.frozen.title + " (read only; esc: back, c: continue from here)"
}

// noteHead is the status line's note that the head moved.
func (m *Model) noteHead(entry string) {
	m.note = "head moved to " + shortID(entry) + " at " + time.Now().Format("15:04:05")
}
