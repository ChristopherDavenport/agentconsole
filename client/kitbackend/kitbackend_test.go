package kitbackend_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChristopherDavenport/agentkit"
	"github.com/ChristopherDavenport/agentpolicy"
	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentsession/jsonl"
	"github.com/ChristopherDavenport/agentskill"
	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client"
	"github.com/ChristopherDavenport/agentconsole/client/kitbackend"
	"github.com/ChristopherDavenport/agentconsole/console"
	"github.com/ChristopherDavenport/agentconsole/internal/inspect"
	"github.com/ChristopherDavenport/agentconsole/internal/scripted"
)

type noArgs struct{}

// danger is a tool the policy asks about; ran counts its executions.
func danger(ran *atomic.Int32) agenttool.Tool {
	return agenttool.New("danger", "does something that needs approval", func(context.Context, noArgs) (string, error) {
		ran.Add(1)
		return "did it", nil
	})
}

func rules(t *testing.T, s string) []agentpolicy.Rule {
	t.Helper()
	r, err := agentpolicy.ParseRules(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

type env struct {
	t     *testing.T
	dir   string
	store *jsonl.Store
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, dir: t.TempDir()}
	e.open()
	return e
}

// open (re)opens the store, as a restarted process does.
func (e *env) open() {
	e.t.Helper()
	if e.store != nil {
		e.store.Close()
	}
	st, err := jsonl.Open(e.dir)
	if err != nil {
		e.t.Fatal(err)
	}
	e.store = st
	e.t.Cleanup(func() { st.Close() })
}

// kit builds a kit that records into the env's store, a new session or
// the one resume names.
func (e *env) kit(resume string, opts ...agentkit.Option) *agentkit.Kit {
	e.t.Helper()
	if resume != "" {
		opts = append(opts, agentkit.WithResumedSession(e.store, resume))
	} else {
		opts = append(opts, agentkit.WithSession(e.store, agentsession.Header{Records: agentsession.AllRecords}))
	}
	k, err := agentkit.New(context.Background(), opts...)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { k.Close() })
	return k
}

func backend(t *testing.T, k *agentkit.Kit, opts ...kitbackend.Option) *kitbackend.Backend {
	t.Helper()
	be, err := kitbackend.New(k, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { be.Close() })
	return be
}

func prompt(t *testing.T, ctl client.Control, text string) {
	t.Helper()
	if err := ctl.Prompt(context.Background(), openresponses.UserText(text)); err != nil {
		t.Fatalf("prompt: %v", err)
	}
}

func pendingIDs(ctl client.Control) []string {
	var ids []string
	for _, p := range ctl.State().Pending {
		ids = append(ids, p.Call.CallID)
	}
	return ids
}

// session reads the followed session as a client does.
func session(t *testing.T, be client.Backend) *agentsession.Session {
	t.Helper()
	s, err := be.Record().Read(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// callDetail is what the client's detail pane shows of the function call
// named name, from the record.
func callDetail(t *testing.T, be client.Backend, name string) inspect.Call {
	t.Helper()
	s := session(t, be)
	var id string
	for _, e := range s.Entries() {
		if it, ok := e.(*agentsession.ItemEntry); ok {
			if fc, ok := it.Item.(*openresponses.FunctionCall); ok && fc.Name == name {
				id = e.Base().ID
			}
		}
	}
	if id == "" {
		t.Fatalf("no call to %s in the record", name)
	}
	d, err := inspect.New(be.Record()).Entry(context.Background(), "", "", id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d.Call == nil {
		t.Fatalf("no call detail for %s", name)
	}
	return *d.Call
}

func lastAssistant(s *agentsession.Session) string {
	var out string
	for _, e := range s.Entries() {
		if it, ok := e.(*agentsession.ItemEntry); ok {
			if m, ok := it.Item.(*openresponses.Message); ok && m.Role == openresponses.RoleAssistant {
				out = m.Text()
			}
		}
	}
	return out
}

// askDanger is a policy that asks about danger and allows the rest.
func askDanger(t *testing.T) agentkit.Option {
	return agentkit.WithPolicy(agentpolicy.Policy{Ask: rules(t, "danger"), Default: agentpolicy.Allow()}, nil)
}

// TestApproveThroughControlAnswer: a call the policy asks about leaves the
// run input-required and unrun; approving it through Control.Answer runs
// it, and the record says who decided.
func TestApproveThroughControlAnswer(t *testing.T) {
	var ran atomic.Int32
	m := scripted.New(scripted.Call("c1", "danger", `{}`), scripted.Say("all done"))
	e := newEnv(t)
	be := backend(t, e.kit("", agentkit.WithModel(m, "scripted"), agentkit.WithTools(danger(&ran)), askDanger(t)))
	ctl := be.Control()

	prompt(t, ctl, "do it")
	if got := pendingIDs(ctl); len(got) != 1 || got[0] != "c1" {
		t.Fatalf("pending = %v, want c1", got)
	}
	if ran.Load() != 0 {
		t.Fatal("the tool ran before it was approved")
	}
	if err := ctl.Answer(context.Background(), agentturn.Approve("c1").WithBy(agentpolicy.ByHuman)); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if ran.Load() != 1 {
		t.Fatalf("tool ran %d times after approval, want 1", ran.Load())
	}
	if got := lastAssistant(session(t, be)); got != "all done" {
		t.Fatalf("the run ended with %q", got)
	}
	c := callDetail(t, be, "danger")
	// The record holds the hold the policy made and then the person's
	// answer, who gave it.
	if n := len(c.Decisions); n != 2 || c.Decisions[0].Verdict != "hold" || c.Decisions[0].By != agentpolicy.ByPolicy ||
		c.Decisions[1].Verdict != "proceed" || c.Decisions[1].By != agentpolicy.ByHuman {
		t.Errorf("the record's decisions for the call = %+v, want the policy's hold then a human's proceed", c.Decisions)
	}
}

// TestRefuseThroughControlAnswer: a refusal is the call's output, and the
// tool never runs.
func TestRefuseThroughControlAnswer(t *testing.T) {
	var ran atomic.Int32
	m := scripted.New(scripted.Call("c1", "danger", `{}`), scripted.Say("understood"))
	e := newEnv(t)
	be := backend(t, e.kit("", agentkit.WithModel(m, "scripted"), agentkit.WithTools(danger(&ran)), askDanger(t)))
	ctl := be.Control()
	prompt(t, ctl, "do it")
	refusal := agentturn.Refuse(openresponses.NewFunctionCallOutput("c1", "The user refused this call.")).WithBy(agentpolicy.ByHuman)
	if err := ctl.Answer(context.Background(), refusal); err != nil {
		t.Fatal(err)
	}
	if ran.Load() != 0 {
		t.Error("a refused call ran")
	}
	if c := callDetail(t, be, "danger"); !strings.Contains(c.Output, "refused") {
		t.Errorf("output = %q, want the refusal", c.Output)
	}
}

// both is a model step that calls two tools in one response.
func both(a, b string) scripted.Step {
	return func(ctx context.Context, em *openresponses.Emitter) error {
		for i, name := range []string{a, b} {
			id := []string{"c1", "c2"}[i]
			w, err := em.FunctionCall(id, name)
			if err != nil {
				return err
			}
			if err := w.Arguments(`{}`); err != nil {
				return err
			}
			if err := w.Close(); err != nil {
				return err
			}
		}
		return nil
	}
}

// TestAnswerReleasesTheCallsTheEngineHeld: of two calls in a batch the
// policy asks about only one; answering that one releases the other
// through the engine, as Control.Answer is documented to.
func TestAnswerReleasesTheCallsTheEngineHeld(t *testing.T) {
	var ranDanger, ranSafe atomic.Int32
	safe := agenttool.New("safe", "harmless", func(context.Context, noArgs) (string, error) {
		ranSafe.Add(1)
		return "fine", nil
	})
	m := scripted.New(both("danger", "safe"), scripted.Say("both done"))
	e := newEnv(t)
	be := backend(t, e.kit("", agentkit.WithModel(m, "scripted"), agentkit.WithTools(danger(&ranDanger), safe), askDanger(t)))
	ctl := be.Control()
	prompt(t, ctl, "go")
	ids := pendingIDs(ctl)
	if len(ids) != 2 {
		t.Fatalf("pending = %v, want both calls held", ids)
	}
	if err := ctl.Answer(context.Background(), agentturn.Approve("c1").WithBy(agentpolicy.ByHuman)); err != nil {
		t.Fatalf("answering only the asked call: %v", err)
	}
	if ranDanger.Load() != 1 || ranSafe.Load() != 1 {
		t.Fatalf("danger ran %d, safe ran %d; want both once", ranDanger.Load(), ranSafe.Load())
	}
	s := callDetail(t, be, "safe")
	var released bool
	for _, v := range s.Verdicts {
		released = released || v.Held
	}
	if !released {
		t.Errorf("the record holds no released-hold verdict for safe: %+v", s.Verdicts)
	}
}

// skillDir writes a skill whose allowed-tools names danger.
func skillDir(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(filepath.Join(root, "digging"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: digging\ndescription: how to dig\nallowed-tools: danger\n---\n\ndig carefully\n"
	if err := os.WriteFile(filepath.Join(root, "digging", "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func skillKit(t *testing.T, e *env, resume string, m *scripted.Model, ran *atomic.Int32) *agentkit.Kit {
	return e.kit(resume,
		agentkit.WithModel(m, "scripted"),
		agentkit.WithTools(danger(ran)),
		agentkit.WithSkills(skillDir(t)),
		agentkit.WithPolicy(agentpolicy.Policy{Allow: rules(t, "skill"), Default: agentpolicy.Ask()}, nil),
		agentkit.WithSkillGrants(func(sk *agentskill.Skill) agentpolicy.Source {
			return agentpolicy.Source{Name: "skill:" + sk.ListedName(), Path: sk.Location, Trusted: true}
		}),
		agentkit.WithSkillGrantScope(),
	)
}

// TestASkillReadGrantsAToolAndTheRecordShowsIt: the model reads a skill
// whose allowed-tools names a tool the policy would ask about; the call
// runs without a question, and the record's detail for it names the
// skill whose grant allowed it, under the conversation's grant scope.
func TestASkillReadGrantsAToolAndTheRecordShowsIt(t *testing.T) {
	var ran atomic.Int32
	m := scripted.New(
		scripted.Call("s1", agentskill.ToolName, `{"name":"digging"}`),
		scripted.Call("c1", "danger", `{}`),
		scripted.Say("dug"),
	)
	e := newEnv(t)
	be := backend(t, skillKit(t, e, "", m, &ran))
	prompt(t, be.Control(), "dig")
	if got := pendingIDs(be.Control()); len(got) != 0 {
		t.Fatalf("the granted call was asked about: %v", got)
	}
	if ran.Load() != 1 {
		t.Fatalf("danger ran %d times, want 1", ran.Load())
	}
	c := callDetail(t, be, "danger")
	if len(c.Grants) != 1 || c.Grants[0].Skill != "digging" || c.Grants[0].Ended != "" {
		t.Fatalf("grants in force for the call = %+v, want digging's", c.Grants)
	}
	// The grant is the conversation's: another scope's call is not
	// allowed by it.
	kit := be.Kit()
	ctx := agentpolicy.ContextWithGrantScope(context.Background(), kit.GrantScope(context.Background()))
	if len(kit.Engine().GrantsFor(ctx)) == 0 {
		t.Error("the engine holds no grant under the conversation's scope")
	}
	other := agentpolicy.ContextWithGrantScope(context.Background(), "another conversation")
	if n := len(kit.Engine().GrantsFor(other)); n != 0 {
		t.Errorf("another conversation holds %d grants", n)
	}
}

// entryOf finds the id of the first item entry satisfying match.
func entryOf(t *testing.T, s *agentsession.Session, match func(openresponses.Item) bool) string {
	t.Helper()
	for _, e := range s.Entries() {
		if it, ok := e.(*agentsession.ItemEntry); ok && match(it.Item) {
			return e.Base().ID
		}
	}
	t.Fatal("no such entry")
	return ""
}

// TestContinueFromMovesTheSkillGrants: moving the head back to before the
// skill read ends its grant, and moving forward to after it grants it
// again.
func TestContinueFromMovesTheSkillGrants(t *testing.T) {
	var ran atomic.Int32
	m := scripted.New(
		scripted.Call("s1", agentskill.ToolName, `{"name":"digging"}`),
		scripted.Say("read it"),
	)
	e := newEnv(t)
	be := backend(t, skillKit(t, e, "", m, &ran))
	ctl := be.Control()
	prompt(t, ctl, "learn")

	kit := be.Kit()
	grants := func() int {
		ctx := agentpolicy.ContextWithGrantScope(context.Background(), kit.GrantScope(context.Background()))
		return len(kit.Engine().GrantsFor(ctx))
	}
	if grants() == 0 {
		t.Fatal("no grant after the read")
	}
	s := session(t, be)
	user := entryOf(t, s, func(it openresponses.Item) bool {
		m, ok := it.(*openresponses.Message)
		return ok && m.Role == openresponses.RoleUser
	})
	after := entryOf(t, s, func(it openresponses.Item) bool {
		o, ok := it.(*openresponses.FunctionCallOutput)
		return ok && o.CallID == "s1"
	})
	ctx := context.Background()
	if err := ctl.ContinueFrom(ctx, user); err != nil {
		t.Fatalf("continue from the prompt: %v", err)
	}
	if n := grants(); n != 0 {
		t.Fatalf("%d grants after moving to before the read, want 0", n)
	}
	if err := ctl.ContinueFrom(ctx, after); err != nil {
		t.Fatalf("continue from the read's output: %v", err)
	}
	if grants() == 0 {
		t.Fatal("the grant was not made again moving to after the read")
	}
}

// TestResumeAnswersACallHeldBeforeARestart: a kit resumed from the session
// restores the call that was awaiting approval; Control.Answer approves it
// and the run goes on.
func TestResumeAnswersACallHeldBeforeARestart(t *testing.T) {
	var ran atomic.Int32
	e := newEnv(t)
	m1 := scripted.New(scripted.Call("c1", "danger", `{}`))
	k1 := e.kit("", agentkit.WithModel(m1, "scripted"), agentkit.WithTools(danger(&ran)), askDanger(t))
	be1 := backend(t, k1)
	prompt(t, be1.Control(), "do it")
	if got := pendingIDs(be1.Control()); len(got) != 1 {
		t.Fatalf("pending before the restart = %v", got)
	}
	id := k1.SessionID()
	be1.Close()
	k1.Close()

	// A new process: the store is opened again, the kit resumes.
	e.open()
	m2 := scripted.New(scripted.Say("welcome back"))
	k2 := e.kit(id, agentkit.WithModel(m2, "scripted"), agentkit.WithTools(danger(&ran)), askDanger(t))
	be2 := backend(t, k2)
	ctl := be2.Control()
	if got := pendingIDs(ctl); len(got) != 1 || got[0] != "c1" {
		t.Fatalf("pending after the restart = %v, want c1", got)
	}
	if ran.Load() != 0 {
		t.Fatal("the call ran before it was answered")
	}
	if err := ctl.Answer(context.Background(), agentturn.Approve("c1").WithBy(agentpolicy.ByHuman)); err != nil {
		t.Fatalf("answer after the restart: %v", err)
	}
	if ran.Load() != 1 {
		t.Fatalf("tool ran %d times, want 1", ran.Load())
	}
	if got := lastAssistant(session(t, be2)); got != "welcome back" {
		t.Fatalf("the resumed run ended with %q", got)
	}
	if be2.SessionID() != id {
		t.Errorf("the backend follows %s, want the resumed %s", be2.SessionID(), id)
	}
}

// TestAToolsQuestionIsRecordedUnderItsCall: the tool asks mid-call; the
// kit's elicitor answers, and the question and answer are filed under the
// call, in the session the client follows.
func TestAToolsQuestionIsRecordedUnderItsCall(t *testing.T) {
	ask := agenttool.New("ask", "asks a question", func(ctx context.Context, _ noArgs) (string, error) {
		q, ok := agenttool.ElicitorFrom(ctx)
		if !ok {
			return "no elicitor", nil
		}
		a, err := q(ctx, agenttool.Elicitation{Message: "proceed?"})
		if err != nil {
			return "", err
		}
		return string(a.Action), nil
	})
	var asked atomic.Int32
	m := scripted.New(scripted.Call("c1", "ask", `{}`), scripted.Say("ok"))
	e := newEnv(t)
	be := backend(t, e.kit("", agentkit.WithModel(m, "scripted"), agentkit.WithTools(ask),
		agentkit.WithToolElicitor(agentpolicy.ByHuman, func(_ context.Context, q agenttool.Elicitation) (agenttool.Answer, error) {
			asked.Add(1)
			return agenttool.Answer{Action: agenttool.ActionDecline}, nil
		})))
	prompt(t, be.Control(), "ask")
	if asked.Load() != 1 {
		t.Fatalf("elicitor called %d times", asked.Load())
	}
	c := callDetail(t, be, "ask")
	if len(c.Asked) != 2 || c.Asked[0].Phase != "ask" || c.Asked[0].Message != "proceed?" ||
		c.Asked[1].Phase != "answer" || c.Asked[1].Action != "decline" || c.Asked[1].By != agentpolicy.ByHuman {
		t.Fatalf("questions recorded under the call = %+v, want the ask and the human's decline", c.Asked)
	}
	if c.Output != "decline" {
		t.Errorf("output = %q, want the tool to have seen the decline", c.Output)
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestConsoleApprovesAKitCallFromTheTerminal: console.Run over the kit
// backend, on a pipe. The permission is shown, y approves it, and the
// tool's reply reaches the screen.
func TestConsoleApprovesAKitCallFromTheTerminal(t *testing.T) {
	var ran atomic.Int32
	m := scripted.New(scripted.Call("c1", "danger", `{}`), scripted.Say("tool said ok"))
	e := newEnv(t)
	be := backend(t, e.kit("", agentkit.WithModel(m, "scripted"), agentkit.WithTools(danger(&ran)), askDanger(t)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pr, pw := io.Pipe()
	defer pw.Close()
	out := &syncBuf{}
	done := make(chan error, 1)
	go func() {
		done <- console.Run(ctx, be, console.WithInput(pr), console.WithOutput(out), console.WithWindowSize(100, 30), console.WithoutSignalHandler())
	}()
	wait := func(sub string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !strings.Contains(out.String(), sub) {
			if time.Now().After(deadline) {
				t.Fatalf("%q never shown; output:\n%q", sub, out.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	pw.Write([]byte("run it\r"))
	wait("Permission requested")
	if ran.Load() != 0 {
		t.Fatal("the tool ran before approval")
	}
	pw.Write([]byte("y"))
	wait("tool said ok")
	if ran.Load() != 1 {
		t.Fatalf("tool ran %d times", ran.Load())
	}
	for be.Control().State().Running {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	pw.Write([]byte{0x03})
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ctrl+c did not end Run")
	}
	c := callDetail(t, be, "danger")
	if len(c.Decisions) == 0 || c.Decisions[len(c.Decisions)-1].By != agentpolicy.ByHuman {
		t.Errorf("decisions = %+v, want the person's", c.Decisions)
	}
}

func TestNewNeedsASession(t *testing.T) {
	k, err := agentkit.New(context.Background(), agentkit.WithModel(scripted.New(), "m"))
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if _, err := kitbackend.New(k); err == nil {
		t.Fatal("a kit without a session made a backend")
	}
	if _, err := kitbackend.New(nil); err == nil {
		t.Fatal("a nil kit made a backend")
	}
}
