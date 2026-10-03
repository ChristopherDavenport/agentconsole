package native_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentsession/jsonl"
	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client/native"
	"github.com/ChristopherDavenport/agentconsole/view"
)

// spy records the user and assistant text each request carried.
type spy struct {
	mu   sync.Mutex
	next openresponses.Streamer
	sent [][]string
}

func (s *spy) CreateStream(ctx context.Context, req openresponses.Request, sink openresponses.EventSink) error {
	var texts []string
	for _, it := range req.Input {
		if m, ok := it.(*openresponses.Message); ok {
			texts = append(texts, m.Text())
		}
	}
	s.mu.Lock()
	s.sent = append(s.sent, texts)
	s.mu.Unlock()
	return s.next.CreateStream(ctx, req, sink)
}

func (s *spy) last() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sent[len(s.sent)-1]
}

func entryOf(m view.Model, want string) string {
	for _, row := range m.Rows {
		if text(row.Item) == want {
			return row.EntryID
		}
	}
	return ""
}

// TestContinueFromMovesTheHead continues from the first answer after a
// second exchange: the next request carries the first exchange and the new
// prompt and not the second exchange, the record gains a branch with the
// head on it (a leaf label), and a session reopened from the record
// resumes there.
func TestContinueFromMovesTheHead(t *testing.T) {
	sp := &spy{next: &script{responses: []func(context.Context, *openresponses.Emitter) error{
		say(nil, nil, "r1"), say(nil, nil, "r2"), say(nil, nil, "r3"),
	}}}
	r := newRig(t, agentturn.Config{Model: sp, ModelName: "scripted"})
	r.finish(r.prompt("one"))
	r.waitFor("r1", func(m view.Model) bool { return idle(m) && entryOf(m, "r1") != "" })
	r.finish(r.prompt("two"))
	m := r.waitFor("r2", func(m view.Model) bool { return idle(m) && entryOf(m, "r2") != "" })
	r1 := entryOf(m, "r1")
	r.mu.Lock()
	r.allowDrop = true // the view leaves the abandoned line
	r.mu.Unlock()

	if err := r.ctl.ContinueFrom(r.ctx, r1); err != nil {
		t.Fatal(err)
	}
	m = r.waitFor("the head on r1", func(m view.Model) bool { return m.Leaf == r1 })
	if len(m.Rows) != 2 || text(m.Rows[1].Item) != "r1" {
		t.Errorf("rows after the move = %s", describe(m))
	}

	r.finish(r.prompt("three"))
	m = r.waitFor("r3", func(m view.Model) bool { return idle(m) && entryOf(m, "r3") != "" })
	if got := sp.last(); !slices.Equal(got, []string{"one", "r1", "three"}) {
		t.Errorf("the request carried %v, want the first exchange and the new prompt", got)
	}
	if len(m.Branches) != 2 {
		t.Errorf("branches = %+v, want the abandoned one and the new one", m.Branches)
	}
	var texts []string
	for _, row := range m.Rows {
		texts = append(texts, text(row.Item))
	}
	if !slices.Equal(texts, []string{"one", "r1", "three", "r3"}) {
		t.Errorf("the viewed line = %v", texts)
	}

	// The head is on the record: a reader that resolves the leaf from the
	// file ends on the new branch, and the label is there.
	s, err := r.be.Record().Read(r.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	labels := 0
	for _, e := range s.Entries() {
		if l, ok := e.(*agentsession.LabelEntry); ok && l.Label != nil && *l.Label == agentsession.LeafLabel {
			labels++
		}
	}
	if labels == 0 {
		t.Error("no leaf label on the record")
	}
	var line []string
	for _, e := range s.Path(s.Leaf()) {
		if ie, ok := e.(*agentsession.ItemEntry); ok {
			line = append(line, text(ie.Item))
		}
	}
	if !slices.Equal(line, []string{"one", "r1", "three", "r3"}) {
		t.Errorf("the record's own leaf is on %v", line)
	}
}

func TestContinueFromRefusesWhileARunGoes(t *testing.T) {
	g := newGates()
	r := newRig(t, agentturn.Config{ModelName: "scripted", Model: &script{responses: []func(context.Context, *openresponses.Emitter) error{
		say(g, map[int]string{0: "mid"}, "x", "y"),
	}}})
	t.Cleanup(func() { g.release("mid") })
	run := r.prompt("go")
	g.arrive(t, "mid")
	err := r.ctl.ContinueFrom(r.ctx, "whatever")
	if err == nil || !strings.Contains(err.Error(), "running") && !strings.Contains(err.Error(), "run") {
		t.Errorf("err = %v, want a refusal while running", err)
	}
	g.release("mid")
	r.finish(run)
}

func TestContinueFromAnUnknownEntryFails(t *testing.T) {
	r := newRig(t, agentturn.Config{ModelName: "scripted", Model: &script{responses: []func(context.Context, *openresponses.Emitter) error{say(nil, nil, "a")}}})
	r.finish(r.prompt("hi"))
	if err := r.ctl.ContinueFrom(r.ctx, "nope"); err == nil {
		t.Error("continuing from an entry the session lacks succeeded")
	}
	if err := r.ctl.ContinueFrom(r.ctx, ""); err == nil {
		t.Error("continuing from no entry succeeded")
	}
}

// A fork's prefix above its base is not a place the leaf may rest, and
// ContinueFrom said so only after the recorder had moved: the error, and
// every prompt after it, was agentsession's base rule. The target is
// refused first and the session goes on.
func TestContinueFromAForksPrefixIsRefusedAndTheForkStillWorks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := jsonl.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	origin, err := store.Create(ctx, agentsession.Header{Records: agentsession.AllRecords})
	if err != nil {
		t.Fatal(err)
	}
	add := func(role, txt string) string {
		m := openresponses.UserText(txt)
		if role == "assistant" {
			m.Role = openresponses.RoleAssistant
		}
		id, err := store.Append(ctx, origin.ID(), agentsession.NewItemEntry(m))
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	above := add("user", "origin question")
	base := add("assistant", "origin answer")
	rec, _, err := session.Start(ctx, store, agentsession.Header{Records: agentsession.AllRecords, ParentSession: origin.ID(), Base: base})
	if err != nil {
		t.Fatal(err)
	}
	sp := &spy{next: &script{responses: []func(context.Context, *openresponses.Emitter) error{say(nil, nil, "fork answer"), say(nil, nil, "again")}}}
	ag := agentturn.New(agentturn.Config{Model: sp, ModelName: "m"})
	defer rec.Attach(ag)()
	be, err := native.New(ag, rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := be.Control().ContinueFrom(ctx, above); err == nil || !strings.Contains(err.Error(), "can only continue from its base") {
		t.Fatalf("err = %v, want a refusal naming the prefix", err)
	}
	if err := be.Control().Prompt(ctx, openresponses.UserText("hi")); err != nil {
		t.Fatalf("the fork is broken after the refusal: %v", err)
	}
	// The base itself is a place to continue from.
	if err := be.Control().ContinueFrom(ctx, base); err != nil {
		t.Fatalf("continue from the base: %v", err)
	}
	if err := be.Control().Prompt(ctx, openresponses.UserText("again?")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sp.last(), "|"); got != "origin question|origin answer|again?" {
		t.Errorf("request = %q", got)
	}
}

func TestContinueFromALeafLabelIsRefused(t *testing.T) {
	r := newRig(t, agentturn.Config{ModelName: "m", Model: &script{responses: []func(context.Context, *openresponses.Emitter) error{say(nil, nil, "a"), say(nil, nil, "b")}}})
	r.finish(r.prompt("one"))
	m := r.waitFor("a", func(m view.Model) bool { return idle(m) && entryOf(m, "a") != "" })
	if err := r.ctl.ContinueFrom(r.ctx, entryOf(m, "a")); err != nil {
		t.Fatal(err)
	}
	s, _ := r.be.Record().Read(r.ctx, "")
	var label string
	for _, e := range s.Entries() {
		if l, ok := e.(*agentsession.LabelEntry); ok && l.Label != nil {
			label = l.ID
		}
	}
	if label == "" {
		t.Fatal("no label to try")
	}
	if err := r.ctl.ContinueFrom(r.ctx, label); err == nil || !strings.Contains(err.Error(), "leaf label") {
		t.Errorf("err = %v, want a refusal of the label", err)
	}
	r.finish(r.prompt("two"))
}

// A step that fails after the recorder moved puts the old leaf, transcript
// and pending calls back: the next prompt continues the old line.
func TestAFailedMovePutsTheHeadBack(t *testing.T) {
	sp := &spy{next: &script{responses: []func(context.Context, *openresponses.Emitter) error{
		say(nil, nil, "r1"), say(nil, nil, "r2"), say(nil, nil, "r3"),
	}}}
	r := newRig(t, agentturn.Config{Model: sp, ModelName: "scripted"})
	r.finish(r.prompt("one"))
	r.waitFor("r1", func(m view.Model) bool { return idle(m) && entryOf(m, "r1") != "" })
	r.finish(r.prompt("two"))
	m := r.waitFor("r2", func(m view.Model) bool { return idle(m) && entryOf(m, "r2") != "" })
	before := r.sess.Leaf()
	r.mu.Lock()
	r.allowDrop = true // the follower sees the head at r1 for a moment
	r.mu.Unlock()

	r.be.FailAfterRebase(func() error { return errors.New("injected") })
	err := r.ctl.ContinueFrom(r.ctx, entryOf(m, "r1"))
	if err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "putting the head back failed") {
		t.Errorf("the undo failed: %v", err)
	}
	r.be.FailAfterRebase(nil)
	r.waitFor("the old line back in view", func(m view.Model) bool { return entryOf(m, "r2") != "" })
	if got := r.sess.Leaf(); got != before {
		t.Errorf("leaf = %s, want the old %s", got, before)
	}
	r.finish(r.prompt("three"))
	if got := strings.Join(sp.last(), "|"); got != "one|r1|two|r2|three" {
		t.Errorf("the next request carried %q, want the old line", got)
	}
}

// Continuing from a call whose output is further down the old line would
// leave the agent with a pending call nothing can answer: refused.
func TestContinueFromACallWithoutItsOutputIsRefused(t *testing.T) {
	cfg := agentturn.Config{ModelName: "m", Tools: []agenttool.Tool{upperTool(nil)},
		Model: &script{responses: []func(context.Context, *openresponses.Emitter) error{callTool("call_1", "upper", `{"text":"a"}`), say(nil, nil, "done"), say(nil, nil, "next")}}}
	r := newRig(t, cfg)
	r.finish(r.prompt("go"))
	m := r.waitFor("done", func(m view.Model) bool { return idle(m) && entryOf(m, "done") != "" })
	var call string
	for _, row := range m.Rows {
		if row.Call != nil {
			call = row.EntryID
		}
	}
	err := r.ctl.ContinueFrom(r.ctx, call)
	if err == nil || !strings.Contains(err.Error(), "without an output") {
		t.Fatalf("err = %v, want a refusal", err)
	}
	r.finish(r.prompt("still works"))
}

// The host's hooks run around a move: before while the recorder still
// writes the branch being left, after on the new one, and a failed move
// gives the host its state back, with the session on the old branch.
func TestHeadMoveHooksRunAroundTheMove(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	var afterLeaf []string
	hooks := native.WithHeadMove(
		func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, "before")
			return nil
		},
		func(_ context.Context, s *agentsession.Session) error {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, "after")
			afterLeaf = append(afterLeaf, s.Leaf())
			return nil
		})
	r := newRig(t, agentturn.Config{Model: &script{responses: []func(context.Context, *openresponses.Emitter) error{
		say(nil, nil, "r1"), say(nil, nil, "r2"),
	}}, ModelName: "scripted"}, hooks)
	r.finish(r.prompt("one"))
	r.waitFor("r1", func(m view.Model) bool { return idle(m) && entryOf(m, "r1") != "" })
	r.finish(r.prompt("two"))
	m := r.waitFor("r2", func(m view.Model) bool { return idle(m) && entryOf(m, "r2") != "" })
	r1 := entryOf(m, "r1")
	r.mu.Lock()
	r.allowDrop = true
	r.mu.Unlock()
	old := r.sess.Leaf()

	if err := r.ctl.ContinueFrom(r.ctx, r1); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if got := strings.Join(calls, ","); got != "before,after" {
		t.Errorf("hooks = %s, want before then after", got)
	}
	if afterLeaf[0] == old {
		t.Errorf("after saw the old leaf %s, want the new branch's", old)
	}
	moved := afterLeaf[0]
	calls, afterLeaf = nil, nil
	mu.Unlock()

	// A failure after the move undoes it: the host is told to end its
	// state at the new branch and to rebuild it on the old.
	r.be.FailAfterRebase(func() error { return errors.New("injected") })
	if err := r.ctl.ContinueFrom(r.ctx, old); err == nil {
		t.Fatal("the move did not fail")
	}
	r.be.FailAfterRebase(nil)
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(calls, ","); got != "before,before,after" {
		t.Errorf("hooks around a failed move = %s, want before, then the undo's before and after", got)
	}
	if len(afterLeaf) != 1 || afterLeaf[0] != moved {
		t.Errorf("the undo's after saw the leaf %v, want the head the move started from, %s", afterLeaf, moved)
	}
}

// A failing before hook refuses the move and nothing changes.
func TestHeadMoveBeforeHookRefuses(t *testing.T) {
	var after int
	hooks := native.WithHeadMove(
		func(context.Context) error { return errors.New("no") },
		func(context.Context, *agentsession.Session) error { after++; return nil })
	r := newRig(t, agentturn.Config{Model: &script{responses: []func(context.Context, *openresponses.Emitter) error{
		say(nil, nil, "r1"), say(nil, nil, "r2"),
	}}, ModelName: "scripted"}, hooks)
	r.finish(r.prompt("one"))
	m := r.waitFor("r1", func(m view.Model) bool { return idle(m) && entryOf(m, "r1") != "" })
	r.finish(r.prompt("two"))
	r.waitFor("r2", func(m view.Model) bool { return idle(m) && entryOf(m, "r2") != "" })
	before := r.sess.Leaf()
	err := r.ctl.ContinueFrom(r.ctx, entryOf(m, "r1"))
	if err == nil || !strings.Contains(err.Error(), "no") {
		t.Fatalf("err = %v", err)
	}
	if after != 0 || r.sess.Leaf() != before {
		t.Errorf("a refused move changed something: after hook %d times, leaf %s, want %s", after, r.sess.Leaf(), before)
	}
}

type ctxKey struct{}

// WithRunContext reaches the tool of a run, and WithRelease sees how the
// last run ended and may change the answers.
func TestRunContextAndReleaseReachTheRun(t *testing.T) {
	var seen atomic.Value
	tool := agenttool.New("act", "acts", func(ctx context.Context, _ struct{}) (string, error) {
		seen.Store(ctx.Value(ctxKey{}))
		return "acted", nil
	})
	var released *agentturn.RunEnd
	opts := []native.Option{
		native.WithRunContext(func(ctx context.Context) context.Context { return context.WithValue(ctx, ctxKey{}, "marked") }),
		native.WithRelease(func(_ context.Context, end *agentturn.RunEnd, answers []agentturn.Answer) ([]agentturn.Answer, error) {
			released = end
			return answers, nil
		}),
	}
	r := newRig(t, agentturn.Config{
		Model: &script{responses: []func(context.Context, *openresponses.Emitter) error{
			callTool("c1", "act", `{}`), say(nil, nil, "done"),
		}},
		ModelName: "scripted",
		Tools:     []agenttool.Tool{tool},
		BeforeToolCall: func(context.Context, agentturn.ToolCallInfo) (*agentturn.ToolDecision, error) {
			return &agentturn.ToolDecision{Action: agentturn.Defer, Reason: "ok?"}, nil
		},
	}, opts...)
	r.finish(r.prompt("go"))
	r.waitFor("the permission", func(m view.Model) bool { return len(m.Permissions) == 1 })
	r.finish(r.start(func() error { return r.ctl.Answer(r.ctx, agentturn.Approve("c1")) }))
	if released == nil || len(released.Pending) != 1 || released.Pending[0].Call.CallID != "c1" {
		t.Fatalf("release saw the end %+v, want the run's, pending c1", released)
	}
	if got := seen.Load(); got != "marked" {
		t.Errorf("the tool's context carried %v, want the run context's mark", got)
	}
}

// If Resume does not start after the release ran, the release's end is
// dropped: the next Answer is released with no end, as after a restart,
// and the error says the calls must be answered again.
func TestAResumeThatDoesNotStartDropsTheEnd(t *testing.T) {
	var mu sync.Mutex
	var ends []*agentturn.RunEnd
	failOnce := true
	opts := []native.Option{native.WithRelease(func(_ context.Context, end *agentturn.RunEnd, answers []agentturn.Answer) ([]agentturn.Answer, error) {
		mu.Lock()
		defer mu.Unlock()
		ends = append(ends, end)
		if failOnce {
			failOnce = false
			// What a release that went on to a Resume that cannot start
			// looks like from here: an answer Resume refuses.
			answers = append(answers[:len(answers):len(answers)], agentturn.Approve("not-pending"))
		}
		return answers, nil
	})}
	r := newRig(t, agentturn.Config{
		Model: &script{responses: []func(context.Context, *openresponses.Emitter) error{
			callTool("c1", "act", `{}`), say(nil, nil, "done"),
		}},
		ModelName: "scripted",
		Tools:     []agenttool.Tool{agenttool.New("act", "acts", func(context.Context, struct{}) (string, error) { return "acted", nil })},
		BeforeToolCall: func(context.Context, agentturn.ToolCallInfo) (*agentturn.ToolDecision, error) {
			return &agentturn.ToolDecision{Action: agentturn.Defer, Reason: "ok?"}, nil
		},
	}, opts...)
	r.finish(r.prompt("go"))
	r.waitFor("the permission", func(m view.Model) bool { return len(m.Permissions) == 1 })

	err := r.ctl.Answer(r.ctx, agentturn.Approve("c1"))
	if err == nil || !strings.Contains(err.Error(), "did not start") {
		t.Fatalf("err = %v, want the run did not start", err)
	}
	r.finish(r.start(func() error { return r.ctl.Answer(r.ctx, agentturn.Approve("c1")) }))
	mu.Lock()
	defer mu.Unlock()
	if len(ends) != 2 || ends[0] == nil || ends[1] != nil {
		t.Fatalf("release saw ends %v, want the run's first and none after the failed start", ends)
	}
}

// Answers Resume would refuse are refused before the release.
func TestAnswersAreCheckedBeforeTheRelease(t *testing.T) {
	var released int
	opts := []native.Option{native.WithRelease(func(_ context.Context, _ *agentturn.RunEnd, a []agentturn.Answer) ([]agentturn.Answer, error) {
		released++
		return a, nil
	})}
	r := newRig(t, agentturn.Config{
		Model:     &script{responses: []func(context.Context, *openresponses.Emitter) error{callTool("c1", "act", `{}`)}},
		ModelName: "scripted",
		Tools:     []agenttool.Tool{agenttool.New("act", "acts", func(context.Context, struct{}) (string, error) { return "acted", nil })},
		BeforeToolCall: func(context.Context, agentturn.ToolCallInfo) (*agentturn.ToolDecision, error) {
			return &agentturn.ToolDecision{Action: agentturn.Defer}, nil
		},
	}, opts...)
	r.finish(r.prompt("go"))
	r.waitFor("the permission", func(m view.Model) bool { return len(m.Permissions) == 1 })
	for _, answers := range [][]agentturn.Answer{
		{agentturn.Approve("c1"), agentturn.Approve("zzz")},
		{agentturn.Approve("c1"), agentturn.Approve("c1")},
	} {
		if err := r.ctl.Answer(r.ctx, answers...); err == nil {
			t.Fatal("accepted")
		}
	}
	if released != 0 {
		t.Errorf("release ran %d times for refused answers", released)
	}
}
