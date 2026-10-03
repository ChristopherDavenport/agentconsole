package inspect_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentsession/jsonl"
	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/internal/client"
	"github.com/ChristopherDavenport/agentconsole/internal/client/native"
	"github.com/ChristopherDavenport/agentconsole/internal/inspect"
	"github.com/ChristopherDavenport/agentconsole/internal/view"
)

type step = func(ctx context.Context, em *openresponses.Emitter) error

type script struct {
	mu        sync.Mutex
	responses []step
	n         int
}

func (s *script) CreateStream(ctx context.Context, req openresponses.Request, sink openresponses.EventSink) error {
	s.mu.Lock()
	i := s.n
	s.n++
	s.mu.Unlock()
	if i >= len(s.responses) {
		return fmt.Errorf("script: request %d, only %d responses", i+1, len(s.responses))
	}
	em := openresponses.NewEmitter(sink, openresponses.NewResponse(req))
	if err := s.responses[i](ctx, em); err != nil {
		return err
	}
	return em.Complete()
}

func say(text string) step {
	return func(_ context.Context, em *openresponses.Emitter) error {
		w, err := em.Message(openresponses.PhaseFinalAnswer)
		if err != nil {
			return err
		}
		if err := w.Text(text); err != nil {
			return err
		}
		return w.Close()
	}
}

func callTool(callID, name, args string) step {
	return func(_ context.Context, em *openresponses.Emitter) error {
		w, err := em.FunctionCall(callID, name)
		if err != nil {
			return err
		}
		if err := w.Arguments(args); err != nil {
			return err
		}
		return w.Close()
	}
}

type textArgs struct {
	Text string `json:"text"`
}

func upper() agenttool.Tool {
	return agenttool.New("upper", "uppercase", func(_ context.Context, a textArgs) (string, error) {
		return strings.ToUpper(a.Text), nil
	})
}

type rig struct {
	t     *testing.T
	ctx   context.Context
	store agentsession.Store
	rec   *session.Recorder
	be    *native.Backend
	in    *inspect.Inspector
}

func newRig(t *testing.T, cfg agentturn.Config) *rig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	store, err := jsonl.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rec, _, err := session.Start(ctx, store, agentsession.Header{Records: agentsession.AllRecords, Harness: &agentsession.Harness{Name: "test", Version: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	ag := agentturn.New(cfg)
	detach := rec.Attach(ag)
	be, err := native.New(ag, rec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		detach()
		if err := store.Close(); err != nil {
			t.Errorf("close the store: %v", err)
		}
	})
	return &rig{t: t, ctx: ctx, store: store, rec: rec, be: be, in: inspect.New(be.Record())}
}

func (r *rig) prompt(text string) {
	r.t.Helper()
	if err := r.be.Control().Prompt(r.ctx, openresponses.UserText(text)); err != nil {
		r.t.Fatal(err)
	}
}

// model reads the session as the view would show it.
func (r *rig) model() view.Model {
	r.t.Helper()
	s, err := r.be.Record().Read(r.ctx, "")
	if err != nil {
		r.t.Fatal(err)
	}
	return view.At(s, "")
}

func (r *rig) entry(row view.Row, m view.Model) inspect.Entry {
	r.t.Helper()
	e, err := r.in.Entry(r.ctx, "", m.Tail, row.EntryID, m.Entries)
	if err != nil {
		r.t.Fatal(err)
	}
	return e
}

func rowWith(t *testing.T, m view.Model, pred func(view.Row) bool) view.Row {
	t.Helper()
	for _, row := range m.Rows {
		if pred(row) {
			return row
		}
	}
	t.Fatalf("no such row in %d rows", len(m.Rows))
	return view.Row{}
}

func isAssistant(row view.Row) bool {
	m, ok := row.Item.(*openresponses.Message)
	return ok && m.Role == openresponses.RoleAssistant
}

func isCall(row view.Row) bool { return row.Call != nil }

func joined(lines []string) string { return strings.Join(lines, "\n") }

func TestAResponseVerifiesAndShowsItsModel(t *testing.T) {
	r := newRig(t, agentturn.Config{ModelName: "scripted-1", Model: &script{responses: []step{say("hi")}}})
	r.prompt("hello")
	m := r.model()
	e := r.entry(rowWith(t, m, isAssistant), m)
	if e.Response == nil {
		t.Fatalf("no response for the answer: %+v", e)
	}
	if e.Response.Model != "scripted-1" || e.Response.Verify.State != inspect.Verified || e.Response.Status == "" {
		t.Errorf("response = %+v", e.Response)
	}
	text := joined(e.Lines())
	for _, want := range []string{"assistant message", "model:    scripted-1", "verified", e.ID} {
		if !strings.Contains(text, want) {
			t.Errorf("the detail lacks %q:\n%s", want, text)
		}
	}
}

// A request a Transform edited is not the one the path rebuilds, so the
// recorder writes the response with no hash and says why in an
// agentturn:unhashed entry; the pane quotes the cause.
func TestAnUnhashedResponseSaysWhy(t *testing.T) {
	cfg := agentturn.Config{ModelName: "m", Model: &script{responses: []step{say("hi")}},
		Transform: func(_ context.Context, tr agentturn.Transcript) (agentturn.Transcript, error) {
			return append(tr, openresponses.UserText("an injected reminder")), nil
		}}
	r := newRig(t, cfg)
	r.prompt("hello")
	m := r.model()
	e := r.entry(rowWith(t, m, isAssistant), m)
	if e.Response == nil || e.Response.Verify.State != inspect.Unhashed {
		t.Fatalf("response = %+v, want unhashed", e.Response)
	}
	if !strings.Contains(e.Response.Verify.Why, "differs") || strings.Contains(e.Response.Verify.Why, "no "+session.UnhashedNS) {
		t.Errorf("why = %q, want the cause the record names", e.Response.Verify.Why)
	}
	text := joined(e.Lines())
	if !strings.Contains(text, "UNHASHED") || !strings.Contains(text, e.Response.Verify.Why) {
		t.Errorf("the detail does not say why:\n%s", text)
	}
	s, err := r.in.Session(r.ctx, "", m.Tail, m.Entries)
	if err != nil {
		t.Fatal(err)
	}
	if s.Verify.Unhashed != 1 || s.Verify.Verified != 0 || !s.Verify.OK() || len(s.Verify.Problems) != 1 {
		t.Errorf("verify = %+v", s.Verify)
	}
	if !strings.Contains(joined(s.Lines()), "unhashed") {
		t.Errorf("the session pane does not mention it:\n%s", joined(s.Lines()))
	}
}

func TestADeferredCallShowsItsDecisionAndWhoApprovedIt(t *testing.T) {
	cfg := agentturn.Config{ModelName: "m", Tools: []agenttool.Tool{upper()},
		Model: &script{responses: []step{callTool("call_1", "upper", `{"text":"abc"}`), say("done")}},
		BeforeToolCall: func(context.Context, agentturn.ToolCallInfo) (*agentturn.ToolDecision, error) {
			return &agentturn.ToolDecision{Action: agentturn.Defer, Reason: "may I run upper?"}, nil
		}}
	r := newRig(t, cfg)
	r.prompt("go")
	if err := r.be.Control().Answer(r.ctx, agentturn.Approve("call_1").WithBy("human")); err != nil {
		t.Fatal(err)
	}
	m := r.model()
	e := r.entry(rowWith(t, m, isCall), m)
	c := e.Call
	if c == nil || c.Name != "upper" || !c.HasOutput || c.Output != "ABC" {
		t.Fatalf("call = %+v", c)
	}
	if c.Dispatch == nil {
		t.Error("no dispatch")
	}
	var hold, proceed *inspect.Decision
	for i := range c.Decisions {
		switch c.Decisions[i].Verdict {
		case agentsession.VerdictHold:
			hold = &c.Decisions[i]
		case agentsession.VerdictProceed:
			proceed = &c.Decisions[i]
		}
	}
	if hold == nil || hold.Reason != "may I run upper?" {
		t.Errorf("no hold with the question in %+v", c.Decisions)
	}
	if proceed == nil || proceed.By != "human" {
		t.Errorf("no proceed by human in %+v", c.Decisions)
	}
	text := joined(e.Lines())
	for _, want := range []string{"hold by", "may I run upper?", "proceed by human", "dispatch: to", "ABC", "no skill grant in force"} {
		if !strings.Contains(text, want) {
			t.Errorf("the detail lacks %q:\n%s", want, text)
		}
	}
	// The output row, selected, shows the same call.
	out := rowWith(t, m, func(row view.Row) bool { _, ok := row.Item.(*openresponses.FunctionCallOutput); return ok })
	if oe := r.entry(out, m); oe.Call == nil || oe.Call.CallID != "call_1" {
		t.Errorf("the output's detail has call %+v", oe.Call)
	}
}

func (r *rig) annotate(ns string, data any) string {
	r.t.Helper()
	b, err := json.Marshal(data)
	if err != nil {
		r.t.Fatal(err)
	}
	id, err := r.rec.Annotate(r.ctx, ns, json.RawMessage(b))
	if err != nil {
		r.t.Fatal(err)
	}
	return id
}

func TestVerdictRecordsAndSkillGrantsInForce(t *testing.T) {
	cfg := agentturn.Config{ModelName: "m", Tools: []agenttool.Tool{upper()},
		Model: &script{responses: []step{callTool("call_1", "upper", `{"text":"a"}`), say("one"), callTool("call_2", "upper", `{"text":"b"}`), say("two")}}}
	r := newRig(t, cfg)
	// A grant made before the first call, revoked between the two.
	r.annotate(inspect.VerdictNS, map[string]any{"action": "allow", "rule": "upper", "source": "agentskill:shout", "reason": "granted upper by agentskill:shout", "by": "policy"})
	r.prompt("one")
	r.annotate(inspect.VerdictNS, map[string]any{"action": "block", "reason": "revoked the rules granted by agentskill:shout", "by": "policy"})
	r.prompt("two")

	m := r.model()
	var calls []view.Row
	for _, row := range m.Rows {
		if row.Call != nil {
			calls = append(calls, row)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("%d call rows", len(calls))
	}
	first := r.entry(calls[0], m).Call
	if len(first.Grants) != 1 || first.Grants[0].Skill != "shout" || first.Grants[0].Rule != "upper" || first.Grants[0].Ended == "" {
		t.Errorf("first call grants = %+v, want shout in force and revoked later", first.Grants)
	}
	second := r.entry(calls[1], m).Call
	if len(second.Grants) != 0 {
		t.Errorf("second call grants = %+v, want none: revoked before it", second.Grants)
	}
	text := joined(r.entry(calls[0], m).Lines())
	if !strings.Contains(text, "shout allowed upper") || !strings.Contains(text, "revoked later") {
		t.Errorf("the grant is not shown:\n%s", text)
	}
	// A verdict recorded for the call itself shows its rule.
	r2 := newRig(t, agentturn.Config{ModelName: "m", Tools: []agenttool.Tool{upper()},
		Model: &script{responses: []step{callTool("call_1", "upper", `{"text":"a"}`), say("x")}}})
	r2.annotate(inspect.VerdictNS, map[string]any{"call_id": "call_1", "tool": "upper", "action": "allow", "rule": "upper(*)", "source": "settings.json", "note": "team policy", "by": "policy"})
	r2.prompt("go")
	m2 := r2.model()
	text = joined(r2.entry(rowWith(t, m2, isCall), m2).Lines())
	for _, want := range []string{"allow by rule upper(*) from settings.json", "team policy"} {
		if !strings.Contains(text, want) {
			t.Errorf("the verdict is not shown (%q):\n%s", want, text)
		}
	}
}

func TestACompactionShowsWhatItFolded(t *testing.T) {
	r := newRig(t, agentturn.Config{ModelName: "m", Model: &script{responses: []step{say("r1"), say("r2")}}})
	r.prompt("one")
	r.prompt("two")
	s, err := r.store.Open(r.ctx, r.rec.SessionID())
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
	c, err := s.Compact(keep, func() openresponses.Item {
		m := openresponses.UserText("we said one and r1")
		m.Role = openresponses.RoleAssistant
		return m
	}())
	if err != nil {
		t.Fatal(err)
	}
	c.TokensBefore = 900
	c.Pinned = openresponses.Items{openresponses.UserText("remember this")}
	if _, err := r.store.Append(r.ctx, s.ID(), c); err != nil {
		t.Fatal(err)
	}
	m := r.model()
	row := rowWith(t, m, func(row view.Row) bool { return row.Fold != nil })
	e := r.entry(row, m)
	f := e.Fold
	if f == nil {
		t.Fatalf("no fold: %+v", e)
	}
	if f.FirstKept != keep || f.Folded != 2 || f.SummaryLen != len("we said one and r1") || len(f.Pinned) != 1 || f.TokensBefore != 900 {
		t.Errorf("fold = %+v", f)
	}
	if !strings.Contains(f.FirstKeptDesc, "two") {
		t.Errorf("first kept = %q, want a description with its text", f.FirstKeptDesc)
	}
	text := joined(e.Lines())
	for _, want := range []string{"compaction", "folded:    2 items", "18 chars", "pinned:    1 items", "user message", "about 900"} {
		if !strings.Contains(text, want) {
			t.Errorf("the detail lacks %q:\n%s", want, text)
		}
	}
}

func TestTheSessionPaneListsRefsConfigAndTheManifest(t *testing.T) {
	r := newRig(t, agentturn.Config{ModelName: "m-cfg", Instructions: "be brief", Model: &script{responses: []step{say("hi")}}})
	r.prompt("hello")
	if rs, ok := r.store.(agentsession.RefStore); ok {
		if err := rs.UpdateRef(r.ctx, "work/main", agentsession.RefTarget{}, agentsession.RefTarget{Session: r.rec.SessionID()}, "test"); err != nil {
			t.Fatal(err)
		}
		if err := rs.UpdateRef(r.ctx, "other", agentsession.RefTarget{}, agentsession.RefTarget{Session: "elsewhere"}, "test"); err == nil {
			// A ref to a session the store lacks is refused; either way
			// it must not be listed.
			t.Log("a dangling ref was accepted")
		}
	} else {
		t.Skip("the jsonl store keeps no refs")
	}
	whole := map[string]any{"entries": []map[string]any{
		{"scope": "user", "name": "style", "hash": "sha256:a", "bytes": 100},
		{"scope": "project", "name": "layout", "hash": "sha256:b", "bytes": 250},
	}, "omitted": []map[string]any{{"scope": "user", "name": "big", "hash": "sha256:c", "bytes": 99999, "reason": "budget"}}}
	r.annotate("agentmemory:render", whole)
	m := r.model()
	s, err := r.in.Session(r.ctx, "", m.Tail, m.Entries)
	if err != nil {
		t.Fatal(err)
	}
	if s.Harness != "test 1" || s.ID != r.rec.SessionID() || s.Config.Model != "m-cfg" || !s.Config.Known {
		t.Errorf("header/config = %+v / %+v", s, s.Config)
	}
	if len(s.Refs) != 1 || s.Refs[0].Name != "work/main" {
		t.Errorf("refs = %+v", s.Refs)
	}
	if s.Manifest == nil || len(s.Manifest.Entries) != 2 || len(s.Manifest.Omitted) != 1 {
		t.Fatalf("manifest = %+v", s.Manifest)
	}
	if !s.Verify.OK() || s.Verify.Verified != 1 {
		t.Errorf("verify = %+v", s.Verify)
	}
	text := joined(s.Lines())
	for _, want := range []string{"session " + s.ID, "harness:  test 1", "work/main", "model m-cfg", "2 entries, 1 left out", "user/style", "left out user/big: budget", "verify:   OK"} {
		if !strings.Contains(text, want) {
			t.Errorf("the pane lacks %q:\n%s", want, text)
		}
	}
}

func TestManifestDeltasFold(t *testing.T) {
	r := newRig(t, agentturn.Config{ModelName: "m", Model: &script{responses: []step{say("hi")}}})
	r.prompt("hello")
	e := func(scope, name, hash string, n int) map[string]any {
		return map[string]any{"scope": scope, "name": name, "hash": hash, "bytes": n}
	}
	first := inspect.Manifest{Entries: []inspect.ManifestEntry{
		{Scope: "user", Name: "a", Hash: "sha256:1", Bytes: 1}, {Scope: "user", Name: "b", Hash: "sha256:2", Bytes: 2}, {Scope: "user", Name: "c", Hash: "sha256:3", Bytes: 3}}}
	r.annotate("agentmemory:render", first)
	// b moved: keep a, write b whole, keep c. The hash is what the writer
	// computed; the reader takes the delta's.
	wantHash := inspect.ManifestHash(inspect.Manifest{Entries: []inspect.ManifestEntry{
		{Scope: "user", Name: "a", Hash: "sha256:1", Bytes: 1}, {Scope: "user", Name: "b", Hash: "sha256:9", Bytes: 7}, {Scope: "user", Name: "c", Hash: "sha256:3", Bytes: 3}}})
	r.annotate("agentmemory:render", map[string]any{"base": inspect.ManifestHash(first), "hash": wantHash,
		"entries": []any{map[string]any{"keep": 1}, e("user", "b", "sha256:9", 7), map[string]any{"keep": 1}}})
	m := r.model()
	s, err := r.in.Session(r.ctx, "", m.Tail, m.Entries)
	if err != nil {
		t.Fatal(err)
	}
	if s.Manifest == nil || len(s.Manifest.Entries) != 3 || s.Manifest.Entries[1].Bytes != 7 || s.Manifest.Entries[2].Name != "c" {
		t.Fatalf("manifest = %+v", s.Manifest)
	}
	if s.ManifestSkipped != 0 {
		t.Errorf("skipped %d", s.ManifestSkipped)
	}
	// A delta on a manifest the path does not hold is skipped and counted.
	r.annotate("agentmemory:render", map[string]any{"base": "sha256:nope", "hash": "sha256:x", "entries": []any{map[string]any{"keep": 1}}})
	m = r.model()
	s, _ = r.in.Session(r.ctx, "", m.Tail, m.Entries)
	if s.ManifestSkipped != 1 || len(s.Manifest.Entries) != 3 {
		t.Errorf("skipped %d, entries %d", s.ManifestSkipped, len(s.Manifest.Entries))
	}
}

// What is costly is computed once: a snapshot is reused while the session
// has no more entries, and a response is verified once.
func TestSnapshotsAndVerificationAreCached(t *testing.T) {
	r := newRig(t, agentturn.Config{ModelName: "m", Model: &script{responses: []step{say("a"), say("b")}}})
	r.prompt("one")
	m := r.model()
	for range 3 {
		r.entry(rowWith(t, m, isAssistant), m)
		if _, err := r.in.Session(r.ctx, "", m.Tail, m.Entries); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.in.Reads(); got != 1 {
		t.Errorf("%d reads for one unchanged session, want 1", got)
	}
	r.prompt("two")
	m = r.model()
	r.entry(rowWith(t, m, isAssistant), m)
	if got := r.in.Reads(); got != 2 {
		t.Errorf("%d reads after the session grew, want 2", got)
	}
}

func TestAnEntryOffTheLineIsRefused(t *testing.T) {
	r := newRig(t, agentturn.Config{ModelName: "m", Model: &script{responses: []step{say("a")}}})
	r.prompt("one")
	m := r.model()
	if _, err := r.in.Entry(r.ctx, "", m.Tail, "sha256:nope", m.Entries); err == nil {
		t.Error("an unknown entry was described")
	}
	var _ client.Record = r.be.Record()
}

// Two panes asked for together (a fast Tab Tab) read the session once.
func TestPanesAskedTogetherReadTheSessionOnce(t *testing.T) {
	r := newRig(t, agentturn.Config{ModelName: "m", Model: &script{responses: []step{say("a")}}})
	r.prompt("one")
	m := r.model()
	row := rowWith(t, m, isAssistant)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := r.in.Entry(r.ctx, "", m.Tail, row.EntryID, m.Entries); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := r.in.Session(r.ctx, "", m.Tail, m.Entries); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := r.in.Reads(); got != 1 {
		t.Errorf("%d reads, want 1", got)
	}
}

// Engine.RevokeScope ends every grant of a conversation, and journals it
// as "revoked the rules granted under <scope>" (or without a scope), not as
// a revocation by source.
func TestScopeRevocationEndsEveryGrant(t *testing.T) {
	for _, reason := range []string{"revoked the rules granted under conv-1", "revoked the rules granted without a scope"} {
		t.Run(reason, func(t *testing.T) {
			cfg := agentturn.Config{ModelName: "m", Tools: []agenttool.Tool{upper()},
				Model: &script{responses: []step{callTool("call_1", "upper", `{"text":"a"}`), say("one")}}}
			r := newRig(t, cfg)
			grant := func(src string) {
				r.annotate(inspect.VerdictNS, map[string]any{"action": "allow", "rule": "upper", "source": src, "reason": "granted upper by " + src, "by": "policy"})
			}
			grant("agentskill:one")
			grant("agentskill:two")
			r.prompt("go")
			r.annotate(inspect.VerdictNS, map[string]any{"action": "block", "reason": reason, "by": "policy"})
			m := r.model()
			s, err := r.in.Session(r.ctx, "", m.Tail, m.Entries)
			if err != nil {
				t.Fatal(err)
			}
			if len(s.Grants) != 2 {
				t.Fatalf("grants = %+v", s.Grants)
			}
			for _, g := range s.Grants {
				if g.Ended == "" {
					t.Errorf("grant %+v still in force after %q", g, reason)
				}
			}
			if !strings.Contains(joined(s.Lines()), "revoked at") {
				t.Errorf("the pane does not show the revocation:\n%s", joined(s.Lines()))
			}
		})
	}
}
