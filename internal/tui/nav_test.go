package tui_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"
)

// recordingModel keeps the user and assistant text of each request.
type recordingModel struct {
	mu   sync.Mutex
	next openresponses.Streamer
	sent [][]string
}

func (r *recordingModel) CreateStream(ctx context.Context, req openresponses.Request, sink openresponses.EventSink) error {
	var texts []string
	for _, it := range req.Input {
		if m, ok := it.(*openresponses.Message); ok {
			texts = append(texts, m.Text())
		}
	}
	r.mu.Lock()
	r.sent = append(r.sent, texts)
	r.mu.Unlock()
	return r.next.CreateStream(ctx, req, sink)
}

func (r *recordingModel) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.sent[len(r.sent)-1], "|")
}

func (a *app) press(keys ...tea.KeyType) {
	for _, k := range keys {
		a.key(k)
	}
}

func (a *app) rune(r rune) { a.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}) }

func (a *app) resize(w, h int) { a.send(tea.WindowSizeMsg{Width: w, Height: h}) }

func (a *app) reads() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.m.Reads()
}

// lineWith is the screen line containing sub.
func lineWith(screen, sub string) string {
	for _, l := range strings.Split(screen, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

// exchange has the agent answer and waits for the answer to be committed
// and the run over.
func (a *app) exchange(prompt, answer string) {
	a.t.Helper()
	a.submit(prompt)
	a.waitFor("the answer "+answer, all(has(answer, "idle"), lacks("(streaming)", "(not committed yet)")))
}

// branched is an app whose head was moved back to the first answer and
// continued: the conversation one, r1, three, r3, with the abandoned
// branch one, r1, two, r2 beside it.
func branched(t *testing.T) (*app, *recordingModel) {
	t.Helper()
	rm := &recordingModel{next: &script{responses: []step{
		say(nil, nil, "r1"), say(nil, nil, "r2"), say(nil, nil, "r3"), say(nil, nil, "r4"),
	}}}
	a := newApp(t, agentturn.Config{ModelName: "scripted", Model: rm})
	a.exchange("one", "r1")
	a.exchange("two", "r2")
	// Select r1 (up from the last row: r2, two, r1) and continue there.
	a.press(tea.KeyCtrlP, tea.KeyCtrlP, tea.KeyCtrlP)
	a.waitFor("the cursor", has("▶"))
	a.key(tea.KeyCtrlB)
	s := a.waitFor("the head moved", all(has("head moved to"), lacks("two", "r2")))
	if strings.Contains(s, "▶") {
		t.Errorf("the cursor stayed after the head moved:\n%s", s)
	}
	a.exchange("three", "r3")
	return a, rm
}

func TestBranchThenSwitchBetweenTheConversationAndTheTree(t *testing.T) {
	a, _ := branched(t)

	a.key(tea.KeyCtrlT)
	s := a.waitFor("the tree", has("branches of session", "assistant: r2", "assistant: r3", "tree: up/down"))
	if l := lineWith(s, "assistant: r3"); !strings.Contains(l, "* ") || !strings.Contains(l, "▶") {
		t.Errorf("the head's branch is not marked and selected: %q", l)
	}
	if l := lineWith(s, "assistant: r2"); strings.Contains(l, "* ") {
		t.Errorf("the abandoned branch is marked as the head: %q", l)
	}
	if strings.Contains(s, "> ") && strings.Contains(s, "say something") {
		t.Errorf("the input shows in the tree:\n%s", s)
	}

	// Select the other branch and view it: a read-only look at its line.
	a.press(tea.KeyDown, tea.KeyEnter)
	s = a.waitFor("the abandoned branch", all(has("VIEWING branch", "two", "r2", "read only"), lacks("three", "branches of session")))
	a.typeText("zzz")
	if s2 := a.screen(); strings.Contains(s2, "zzz") {
		t.Errorf("typed into a read-only view:\n%s", s2)
	}
	// The tree is one key away from the frozen view too, and back.
	a.key(tea.KeyCtrlT)
	a.waitFor("the tree again", has("branches of session"))
	a.key(tea.KeyCtrlT)
	a.waitFor("the frozen view again", has("VIEWING branch", "r2"))

	// Esc returns to the live session, where the head still is.
	a.key(tea.KeyEsc)
	s = a.waitFor("the live view", all(has("three", "r3", "idle"), lacks("VIEWING", "r2")))
	if !strings.Contains(s, "say something") && !strings.Contains(s, "> ") {
		t.Errorf("no input back:\n%s", s)
	}
}

func TestContinueFromHereMovesTheHeadAndTheNextPromptContinuesFromIt(t *testing.T) {
	a, rm := branched(t)
	if got := rm.last(); got != "one|r1|three" {
		t.Errorf("after moving the head to r1 the request carried %q, want one|r1|three", got)
	}

	// View the abandoned branch, then continue from it.
	a.key(tea.KeyCtrlT)
	a.waitFor("the tree", has("assistant: r2"))
	a.press(tea.KeyDown, tea.KeyEnter)
	a.waitFor("the abandoned branch", has("VIEWING branch", "r2"))
	a.rune('c')
	s := a.waitFor("the head on it", all(has("head moved to", "r2", "two"), lacks("VIEWING", "three")))
	_ = s
	a.exchange("four", "r4")
	if got := rm.last(); got != "one|r1|two|r2|four" {
		t.Errorf("the request after continuing from r2 carried %q, want one|r1|two|r2|four", got)
	}
	// The tree has the two lines, the head on the new one.
	a.key(tea.KeyCtrlT)
	s = a.waitFor("the tree", has("assistant: r4", "assistant: r3"))
	if l := lineWith(s, "assistant: r4"); !strings.Contains(l, "* ") {
		t.Errorf("the new head is not marked: %q", l)
	}
	if l := lineWith(s, "assistant: r3"); strings.Contains(l, "* ") {
		t.Errorf("the old head is still marked: %q", l)
	}
	if n := strings.Count(s, "assistant: r"); n != 2 {
		t.Errorf("the tree lists %d branches, want 2:\n%s", n, s)
	}
}

func TestContinueFromHereIsRefusedWhileARunGoes(t *testing.T) {
	g := newGates()
	a := newApp(t, cfgWith(say(nil, nil, "r1"), say(g, map[int]string{0: "mid"}, "x", "y")))
	t.Cleanup(func() { g.release("mid") })
	a.exchange("one", "r1")
	a.submit("two")
	g.arrive(t, "mid")
	a.waitFor("running", has("running"))
	a.key(tea.KeyCtrlP)
	a.waitFor("the cursor", has("▶"))
	a.key(tea.KeyCtrlB)
	a.waitFor("the refusal", has("cannot move the head while a run goes"))
	g.release("mid")
	a.waitFor("the run over", all(has("idle", "xy"), lacks("(streaming)")))
}

func TestAForksOriginOpensReadOnly(t *testing.T) {
	var originID, base string
	start := func(ctx context.Context, store agentsession.Store) (*session.Recorder, error) {
		origin, err := store.Create(ctx, agentsession.Header{Records: agentsession.AllRecords})
		if err != nil {
			return nil, err
		}
		originID = origin.ID()
		add := func(role, text, resp string) string {
			m := openresponses.UserText(text)
			if role == "assistant" {
				m.Role = openresponses.RoleAssistant
			}
			e := agentsession.NewItemEntry(m)
			e.ResponseID = resp
			id, err := store.Append(ctx, originID, e)
			if err != nil {
				t.Fatal(err)
			}
			return id
		}
		add("user", "origin question", "")
		base = add("assistant", "origin answer", "r0")
		add("user", "origin later", "")
		rec, _, err := session.Start(ctx, store, agentsession.Header{Records: agentsession.AllRecords, ParentSession: originID, Base: base})
		return rec, err
	}
	a := newAppOn(t, cfgWith(say(nil, nil, "fork answer")), start)
	s := a.waitFor("the fork's prefix", has("origin question", "origin answer"))
	if strings.Contains(s, "origin later") {
		t.Fatalf("the fork shows what its origin wrote after the base:\n%s", s)
	}

	a.key(tea.KeyCtrlT)
	s = a.waitFor("the origin in the tree", has("origin: session", "forked at entry"))
	a.press(tea.KeyDown, tea.KeyEnter) // the branch first, then the origin
	s = a.waitFor("the origin opened", all(has("VIEWING origin", "origin later", "read only"), lacks("branches of session")))
	// The cursor starts on the entry the fork was made at.
	if l := lineWith(s, "▶"); !strings.Contains(l, "assistant") {
		t.Errorf("the cursor is not on the fork point (the origin's answer): %q\n%s", l, s)
	}

	// Read only: nothing typed is sent, and the head cannot be moved in
	// another session.
	a.typeText("hello?")
	a.rune('c')
	s = a.waitFor("the refusal", has("another session"))
	if strings.Contains(s, "hello?") {
		t.Errorf("typed into the origin:\n%s", s)
	}
	a.key(tea.KeyEsc)
	s = a.waitFor("back on the fork", all(has("origin answer", "idle"), lacks("VIEWING", "origin later")))
	_ = originID
}

// callApp is an app whose model calls upper and whose policy defers the
// call, approved by the user.
func callApp(t *testing.T) *app {
	t.Helper()
	cfg := cfgWith(callTool("call_1", "upper", `{"text":"abc"}`), say(nil, nil, "done"))
	cfg.Tools = []agenttool.Tool{upperTool(nil)}
	cfg.BeforeToolCall = deferAll("may I run upper?")
	a := newApp(t, cfg)
	a.resize(110, 50)
	a.submit("go")
	a.waitFor("the permission", has("Permission requested"))
	a.typeText("y")
	a.waitFor("the call done", all(has("upper [ended]", "ABC", "done", "idle")))
	return a
}

func TestDetailPaneShowsADeferredCallsDecisionAndWhoApprovedIt(t *testing.T) {
	a := callApp(t)
	a.press(tea.KeyCtrlP, tea.KeyCtrlP) // the answer, then the call
	a.key(tea.KeyTab)
	s := a.waitFor("the detail", has("record detail", "call call_1 upper", "hold by", "may I run upper?", "proceed by human", "dispatch: to", "ABC"))
	if !strings.Contains(s, "state:    completed") {
		t.Errorf("no call state:\n%s", s)
	}
	// Moving the cursor re-targets the pane.
	a.key(tea.KeyCtrlP)
	a.waitFor("the prompt's detail", has("user message", "response: none"))
	a.key(tea.KeyCtrlN)
	a.waitFor("the call's detail again", has("call call_1 upper"))
	// Tab goes to the session pane, then closes.
	a.key(tea.KeyTab)
	a.waitFor("the session pane", has("session ", "verify:", "harness:  tui-test 9"))
	a.key(tea.KeyTab)
	a.waitFor("no pane", lacks("verify:", "record detail"))
}

func TestAnUnhashedResponseShowsWhy(t *testing.T) {
	cfg := cfgWith(say(nil, nil, "hi"))
	cfg.Transform = func(_ context.Context, tr agentturn.Transcript) (agentturn.Transcript, error) {
		return append(tr, openresponses.UserText("an injected reminder")), nil
	}
	a := newApp(t, cfg)
	a.resize(110, 50)
	a.exchange("hello", "hi")
	a.key(tea.KeyCtrlP) // the answer
	a.key(tea.KeyTab)
	a.waitFor("why", has("UNHASHED", "why: the request's input differs", "model:    scripted"))
	a.key(tea.KeyTab)
	a.waitFor("the session pane", has("verify:", "unhashed"))
}

func TestACompactionsDetailSaysWhatItFolded(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "r1"), say(nil, nil, "r2")))
	a.resize(110, 50)
	a.exchange("one", "r1")
	a.exchange("two", "r2")
	s, err := a.store.Open(a.ctx, a.rec.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	var keep string
	for _, e := range s.Path(s.Leaf()) {
		if ie, ok := e.(*agentsession.ItemEntry); ok {
			if m, ok := ie.Item.(*openresponses.Message); ok && m.Text() == "two" {
				keep = ie.ID
			}
		}
	}
	summary := openresponses.UserText("we said one and r1")
	summary.Role = openresponses.RoleAssistant
	c, err := s.Compact(keep, summary)
	if err != nil {
		t.Fatal(err)
	}
	c.TokensBefore = 800
	c.Pinned = openresponses.Items{openresponses.UserText("remember this")}
	if _, err := a.store.Append(a.ctx, s.ID(), c); err != nil {
		t.Fatal(err)
	}
	a.waitFor("the compaction row", has("[compaction]", "summary 18 chars", "1 pinned"))
	a.key(tea.KeyCtrlP) // the compaction is the last row
	a.key(tea.KeyTab)
	a.waitFor("the fold", has("compaction", "folded:    2 items", "first kept:", "two", "summary:   message, 18 chars", "pinned:    1 items", "about 800"))
}

func TestTheSessionPaneListsRefsAndTheHeader(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hi")))
	a.resize(110, 50)
	rs, ok := a.store.(agentsession.RefStore)
	if !ok {
		t.Skip("the store keeps no refs")
	}
	if err := rs.UpdateRef(a.ctx, "work/main", agentsession.RefTarget{}, agentsession.RefTarget{Session: a.rec.SessionID()}, "test"); err != nil {
		t.Fatal(err)
	}
	a.exchange("hello", "hi")
	a.press(tea.KeyTab, tea.KeyTab)
	a.waitFor("the pane", has("session "+a.rec.SessionID(), "cwd:      /work/dir", "harness:  tui-test 9", "format:   agentsession/", "refs:", "work/main", "verify:   OK", "config:   model scripted"))
}

func TestTheSessionPaneShowsTheMemoryManifest(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hi")))
	a.resize(110, 50)
	a.exchange("hello", "hi")
	data, _ := json.Marshal(map[string]any{"entries": []map[string]any{{"scope": "user", "name": "style", "hash": "sha256:a", "bytes": 10}}})
	if _, err := a.rec.Annotate(a.ctx, "agentmemory:render", json.RawMessage(data)); err != nil {
		t.Fatal(err)
	}
	a.press(tea.KeyTab, tea.KeyTab)
	a.waitFor("the manifest", has("memory:   manifest in force: 1 entries", "user/style"))
}

// What the panes cost is paid when they are asked for, and not on every
// step of the feed: with no pane open the session is never read, and with
// the session pane open a run in flight does not read it again for each
// entry it writes.
func TestPanesAreComputedOnlyWhenAskedForAndNotPerEntry(t *testing.T) {
	g := newGates()
	a := newApp(t, cfgWith(say(nil, nil, "r1"), say(g, map[int]string{0: "mid"}, "x", "y")))
	t.Cleanup(func() { g.release("mid") })
	a.exchange("one", "r1")
	if n := a.reads(); n != 0 {
		t.Fatalf("%d reads with no pane open", n)
	}
	a.press(tea.KeyTab, tea.KeyTab)
	a.waitFor("the session pane", has("verify:"))
	if n := a.reads(); n != 1 {
		t.Fatalf("%d reads for the session pane, want 1", n)
	}
	a.submit("two")
	g.arrive(t, "mid")
	a.waitFor("the run's entries", has("two", "x"))
	if n := a.reads(); n != 1 {
		t.Errorf("%d reads while a run writes its entries, want 1: the pane waits for the run", n)
	}
	g.release("mid")
	a.waitFor("the run over", all(has("xy", "idle"), lacks("(streaming)")))
	a.waitFor("the pane refreshed", func(string) bool { return a.reads() == 2 })
}
