package native_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"
	"github.com/ChristopherDavenport/openresponses/echo"

	"github.com/ChristopherDavenport/agentconsole/internal/view"
)

type textArgs struct {
	Text string `json:"text"`
}

func upperTool(g *gates) agenttool.Tool {
	return agenttool.New("upper", "uppercase", func(ctx context.Context, a textArgs) (string, error) {
		if g != nil {
			agenttool.Progress(ctx, agenttool.Text("working"))
			if err := g.hold(ctx, "tool"); err != nil {
				return "", err
			}
		}
		return strings.ToUpper(a.Text), nil
	})
}

// TestEchoRoundTrip is the smoke test over the stack's own echo adapter:
// the prompt and the answer are on the record and rendered once.
func TestEchoRoundTrip(t *testing.T) {
	r := newRig(t, agentturn.Config{Model: &echo.Adapter{}, ModelName: "echo"})
	r.finish(r.prompt("hello"))
	m := r.waitFor("the answer committed", func(m view.Model) bool { return idle(m) && len(assistantRows(m)) == 1 })
	if got := text(assistantRows(m)[0].Item); got != "hello" {
		t.Errorf("answer = %q, want hello", got)
	}
	if len(m.Rows) != 2 {
		t.Errorf("rows = %d (%s), want the prompt and the answer", len(m.Rows), describe(m))
	}
	if m.Config != "echo" {
		t.Errorf("config model = %q, want echo", m.Config)
	}
	if !r.be.Record().Verified() {
		t.Error("the native record is not verified")
	}
}

// TestStreamingTextIsOverlaidThenCommittedOnce holds the model in the
// middle of a message. The text is on screen as a live row, not on the
// record; once the stream ends the committed item replaces it, once, and
// the rig's invariants say it was never shown twice or dropped in
// between.
func TestStreamingTextIsOverlaidThenCommittedOnce(t *testing.T) {
	g := newGates()
	r := newRig(t, agentturn.Config{ModelName: "m", Model: &script{responses: []func(context.Context, *openresponses.Emitter) error{
		say(g, map[int]string{1: "mid"}, "hel", "lo ", "world"),
	}}})
	run := r.prompt("hi")
	g.arrive(t, "mid")

	m := r.waitFor("the streamed text", func(m view.Model) bool {
		rows := assistantRows(m)
		return len(rows) == 1 && rows[0].Live && text(rows[0].Item) == "hello "
	})
	row := assistantRows(m)[0]
	if !row.Open || row.EntryID != "" {
		t.Errorf("streaming row: open=%v entry=%q, want open and on no entry", row.Open, row.EntryID)
	}
	if m.Turn.State != view.Running || m.Turn.Number != 1 || m.Turn.Model != "m" {
		t.Errorf("turn = %+v, want running turn 1 on m", m.Turn)
	}
	liveID := rowKey(row)

	g.release("mid")
	r.finish(run)
	m = r.waitFor("the committed message", func(m view.Model) bool { return idle(m) && len(assistantRows(m)) == 1 })
	row = assistantRows(m)[0]
	if row.Live || row.EntryID == "" || text(row.Item) != "hello world" {
		t.Errorf("committed row: live=%v entry=%q text=%q", row.Live, row.EntryID, text(row.Item))
	}
	if rowKey(row) != liveID {
		t.Errorf("the committed item is %s, the streamed one was %s", rowKey(row), liveID)
	}
	if m.Turn.State != view.Idle || m.Turn.Number != 0 {
		t.Errorf("turn = %+v, want idle", m.Turn)
	}
	if !r.ever(func(m view.Model) bool {
		rows := assistantRows(m)
		return len(rows) == 1 && rows[0].Live && text(rows[0].Item) == "hello "
	}) {
		t.Error("the streaming overlay was never observed")
	}
}

// TestToolCallLifecycle follows a call from the model's item through the
// dispatch to its output, holding the tool while it runs.
func TestToolCallLifecycle(t *testing.T) {
	g := newGates()
	r := newRig(t, agentturn.Config{ModelName: "m", Tools: []agenttool.Tool{upperTool(g)}, Model: &script{responses: []func(context.Context, *openresponses.Emitter) error{
		callTool("call_1", "upper", `{"text":"abc"}`),
		say(g, nil, "done"),
	}}})
	run := r.prompt("go")
	g.arrive(t, "tool")

	m := r.waitFor("the call running", func(m view.Model) bool {
		c := callRow(m, "call_1")
		return c != nil && c.State == view.CallRunning && c.Partial == "working"
	})
	c := callRow(m, "call_1")
	if c.Committed || c.Output != "" || c.Name != "upper" || c.Args != `{"text":"abc"}` {
		t.Errorf("running call = %+v", *c)
	}
	if m.Turn.State != view.Running {
		t.Errorf("turn = %v, want running", m.Turn.State)
	}

	g.release("tool")
	r.finish(run)
	m = r.waitFor("the output committed", func(m view.Model) bool {
		c := callRow(m, "call_1")
		return idle(m) && c != nil && c.Committed && len(assistantRows(m)) == 1
	})
	c = callRow(m, "call_1")
	if c.State != view.CallEnded || c.Output != "ABC" {
		t.Errorf("finished call = %+v, want ended with ABC", *c)
	}
	var outputs int
	for _, row := range m.Rows {
		if out, ok := row.Item.(*openresponses.FunctionCallOutput); ok && out.CallID == "call_1" {
			outputs++
			if row.Live {
				t.Error("the output row is live")
			}
		}
	}
	if outputs != 1 {
		t.Errorf("output rows = %d, want 1", outputs)
	}
	if !r.ever(func(m view.Model) bool {
		c := callRow(m, "call_1")
		return c != nil && c.State == view.CallEnded && !c.Committed
	}) {
		// The tool's result is shown from the live event until the
		// output lands; the barrier can let the record win outright,
		// so this is a note, not a failure.
		t.Log("the live result was never shown apart from the record")
	}
}

func callRow(m view.Model, callID string) *view.Call {
	for _, row := range m.Rows {
		if row.Call != nil && row.Call.CallID == callID {
			return row.Call
		}
	}
	return nil
}

// TestDeferredCallAsksAndAnswerResumes defers a call to the caller: the
// turn requires action and a permission is out, and approving it through
// Control resumes the run to its end.
func TestDeferredCallAsksAndAnswerResumes(t *testing.T) {
	cfg := agentturn.Config{ModelName: "m", Tools: []agenttool.Tool{upperTool(nil)}, Model: &script{responses: []func(context.Context, *openresponses.Emitter) error{
		callTool("call_1", "upper", `{"text":"abc"}`),
		say(nil, nil, "done"),
	}},
		BeforeToolCall: func(context.Context, agentturn.ToolCallInfo) (*agentturn.ToolDecision, error) {
			return &agentturn.ToolDecision{Action: agentturn.Defer, Reason: "may I run upper?"}, nil
		}}
	r := newRig(t, cfg)
	r.finish(r.prompt("go"))

	m := r.waitFor("the permission", func(m view.Model) bool {
		return m.Turn.State == view.RequiresAction && len(m.Permissions) == 1
	})
	p := m.Permissions[0]
	if p.CallID != "call_1" || p.Name != "upper" || p.Question != "may I run upper?" || p.Reason != string(agentturn.PendingDeferred) {
		t.Errorf("permission = %+v", p)
	}
	if c := callRow(m, "call_1"); c == nil || c.State != view.CallDeferred || c.Committed {
		t.Errorf("call = %+v, want deferred", c)
	}
	if st := r.ctl.State(); len(st.Pending) != 1 {
		t.Errorf("control state pending = %d, want 1", len(st.Pending))
	}

	r.finish(r.start(func() error {
		return r.ctl.Answer(r.ctx, agentturn.Approve("call_1").WithBy("test"))
	}))
	m = r.waitFor("the run resumed to its end", func(m view.Model) bool {
		c := callRow(m, "call_1")
		return idle(m) && c != nil && c.Committed && len(assistantRows(m)) == 1
	})
	if len(m.Permissions) != 0 || m.Turn.State != view.Idle {
		t.Errorf("permissions = %d, turn = %v after the answer", len(m.Permissions), m.Turn.State)
	}
	if c := callRow(m, "call_1"); c.Output != "ABC" || c.State != view.CallEnded {
		t.Errorf("call = %+v, want ended with ABC", *c)
	}
	if !r.ever(func(m view.Model) bool {
		return m.Turn.State == view.Running && len(m.Permissions) == 0 && callRow(m, "call_1") != nil
	}) {
		t.Error("the resumed run was never seen running with the permission cleared")
	}
}

// TestRefusingADeferredCall answers the permission with a refusal: the
// refusal is the output, and the call is answered without running.
func TestRefusingADeferredCall(t *testing.T) {
	cfg := agentturn.Config{ModelName: "m", Tools: []agenttool.Tool{upperTool(nil)}, Model: &script{responses: []func(context.Context, *openresponses.Emitter) error{
		callTool("call_1", "upper", `{"text":"abc"}`),
	}},
		BeforeToolCall: func(context.Context, agentturn.ToolCallInfo) (*agentturn.ToolDecision, error) {
			return &agentturn.ToolDecision{Action: agentturn.Defer}, nil
		}}
	r := newRig(t, cfg)
	r.finish(r.prompt("go"))
	r.waitFor("the permission", func(m view.Model) bool { return len(m.Permissions) == 1 })
	r.finish(r.start(func() error {
		return r.ctl.Answer(r.ctx, agentturn.Refuse(openresponses.NewFunctionCallOutput("call_1", "not allowed")))
	}))
	m := r.waitFor("the refusal committed", func(m view.Model) bool {
		c := callRow(m, "call_1")
		return idle(m) && c != nil && c.Committed
	})
	if c := callRow(m, "call_1"); c.Output != "not allowed" || c.Verdict != agentsession.VerdictReject {
		t.Errorf("call = %+v, want the refusal with a reject verdict", *c)
	}
	if len(m.Permissions) != 0 {
		t.Errorf("permissions = %d, want 0", len(m.Permissions))
	}
}

// TestModelSwitch changes the agent's configuration between two runs:
// the turn runs on the new model, and the record has the config entry
// by the time the answer does.
func TestModelSwitch(t *testing.T) {
	g := newGates()
	model := &script{responses: []func(context.Context, *openresponses.Emitter) error{
		say(nil, nil, "one"),
		say(g, map[int]string{0: "mid"}, "tw", "o"),
	}}
	r := newRig(t, agentturn.Config{Model: model, ModelName: "m1"})
	r.finish(r.prompt("first"))
	r.waitFor("the first answer", func(m view.Model) bool { return idle(m) && len(assistantRows(m)) == 1 && m.Config == "m1" })

	if err := r.agent.SetConfig(agentturn.Config{Model: model, ModelName: "m2"}); err != nil {
		t.Fatal(err)
	}
	run := r.prompt("second")
	g.arrive(t, "mid")
	m := r.waitFor("the second turn streaming", func(m view.Model) bool {
		rows := assistantRows(m)
		return len(rows) == 2 && rows[1].Live
	})
	if m.Turn.Model != "m2" {
		t.Errorf("the turn runs on %q, want m2", m.Turn.Model)
	}

	g.release("mid")
	r.finish(run)
	m = r.waitFor("the new config on the record", func(m view.Model) bool {
		return idle(m) && len(assistantRows(m)) == 2 && m.Config == "m2"
	})
	if got := text(assistantRows(m)[1].Item); got != "two" {
		t.Errorf("second answer = %q, want two", got)
	}
}

// TestModelSwitchInsideARun shows the recorder's first lag. A hook moves
// the second turn of a run to another model; its config entry is written
// by the next entry-writing event, so while the first tokens stream the
// turn runs on the new model and the record still says the old one. The
// view reports both and takes the record's once it has it.
func TestModelSwitchInsideARun(t *testing.T) {
	g := newGates()
	model := &script{responses: []func(context.Context, *openresponses.Emitter) error{
		callTool("call_1", "upper", `{"text":"abc"}`),
		say(g, map[int]string{0: "mid"}, "tw", "o"),
	}}
	var calls int
	r := newRig(t, agentturn.Config{Model: model, ModelName: "m1", Tools: []agenttool.Tool{upperTool(nil)},
		BeforeModelCall: func(_ context.Context, req *openresponses.Request) error {
			calls++
			if calls == 2 {
				req.Model = "m2"
			}
			return nil
		}})
	run := r.prompt("go")
	g.arrive(t, "mid")
	m := r.waitFor("the second turn streaming", func(m view.Model) bool {
		rows := assistantRows(m)
		return len(rows) == 1 && rows[0].Live && m.Turn.Number == 2
	})
	if m.Turn.Model != "m2" {
		t.Errorf("the turn runs on %q, want m2", m.Turn.Model)
	}
	if m.Config != "m1" {
		t.Errorf("the record's model is %q mid-stream, want m1: its entry is not written yet", m.Config)
	}

	g.release("mid")
	r.finish(run)
	m = r.waitFor("the new config on the record", func(m view.Model) bool {
		return idle(m) && len(assistantRows(m)) == 1 && m.Config == "m2"
	})
	if got := text(assistantRows(m)[0].Item); got != "two" {
		t.Errorf("answer = %q, want two", got)
	}
}

// TestItemCompletedBeforeTheStreamNamesItsResponse shows the second lag.
// The stream never names its response, so the recorder holds the
// completed message until response_end: for a while the item is complete
// on the live stream and on no entry, and the overlay must keep it. Then
// the entry lands, naming the response, and the overlay copy goes.
func TestItemCompletedBeforeTheStreamNamesItsResponse(t *testing.T) {
	g := newGates()
	hold := func(ctx context.Context, em *openresponses.Emitter) error {
		w, err := em.Message(openresponses.PhaseFinalAnswer)
		if err != nil {
			return err
		}
		if err := w.Text("settled"); err != nil {
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
		return g.hold(ctx, "before-response-end")
	}
	r := newRig(t, agentturn.Config{ModelName: "m", Model: nameless{&script{responses: []func(context.Context, *openresponses.Emitter) error{hold}}}})
	run := r.prompt("hi")
	g.arrive(t, "before-response-end")

	m := r.waitFor("the completed item overlaid", func(m view.Model) bool {
		rows := assistantRows(m)
		return len(rows) == 1 && rows[0].Live && !rows[0].Open
	})
	row := assistantRows(m)[0]
	if text(row.Item) != "settled" || row.ResponseID != "" {
		t.Errorf("overlay row = %q in response %q, want settled in none yet", text(row.Item), row.ResponseID)
	}
	// The event is not the commit: the view keeps the item. Check the
	// record really holds none.
	for _, e := range r.sess.Entries() {
		if it, ok := e.(*agentsession.ItemEntry); ok {
			if msg, ok := it.Item.(*openresponses.Message); ok && msg.Role == openresponses.RoleAssistant {
				t.Fatalf("the store holds the assistant item before response_end: %s", e.Base().ID)
			}
		}
	}

	g.release("before-response-end")
	r.finish(run)
	m = r.waitFor("the entry landed", func(m view.Model) bool { return idle(m) && len(assistantRows(m)) == 1 })
	row = assistantRows(m)[0]
	if row.Live || row.EntryID == "" || row.ResponseID == "" || text(row.Item) != "settled" {
		t.Errorf("committed row: live=%v entry=%q response=%q", row.Live, row.EntryID, row.ResponseID)
	}
}

// TestBranchAndHead moves a session's head between branches under a
// follower. An append under an earlier entry starts a branch, and with
// no head recorded the newest entry is the leaf, as a read would say; a
// recorded head moves it, and the rendering follows the path there and
// back.
func TestBranchAndHead(t *testing.T) {
	r := newRig(t, agentturn.Config{Model: &echo.Adapter{}, ModelName: "echo"})
	r.allowDrop = true
	r.finish(r.prompt("trunk"))
	m := r.waitFor("the trunk", func(m view.Model) bool { return idle(m) && len(m.Rows) == 2 })
	trunkLeaf, userEntry := m.Leaf, m.Rows[0].EntryID
	if len(m.Leaves) != 1 {
		t.Fatalf("leaves = %v, want one", m.Leaves)
	}

	side := agentsession.NewItemEntry(openresponses.UserText("side"))
	side.Parent = userEntry
	sideID, err := r.store.Append(r.ctx, r.sess.ID(), side)
	if err != nil {
		t.Fatal(err)
	}
	m = r.waitFor("the side branch", func(m view.Model) bool { return m.Leaf == sideID })
	if len(m.Leaves) != 2 || len(m.Rows) != 2 {
		t.Errorf("after the branch: %d leaves, rows %s", len(m.Leaves), describe(m))
	}
	if msg, ok := m.Rows[1].Item.(*openresponses.Message); !ok || msg.Text() != "side" {
		t.Errorf("rows on the side branch = %s", describe(m))
	}

	moveHead := func(to string) {
		t.Helper()
		if err := r.sess.Branch(to); err != nil {
			t.Fatal(err)
		}
		mark, err := r.sess.MarkLeaf()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.store.Append(r.ctx, r.sess.ID(), mark); err != nil {
			t.Fatal(err)
		}
	}
	moveHead(trunkLeaf)
	m = r.waitFor("the head back on the trunk", func(m view.Model) bool { return m.Leaf == trunkLeaf })
	if len(m.Rows) != 2 || len(assistantRows(m)) != 1 || text(assistantRows(m)[0].Item) != "trunk" {
		t.Errorf("rows back on the trunk = %s", describe(m))
	}
	if len(m.Leaves) != 2 {
		t.Errorf("leaves = %v, want both branch tips", m.Leaves)
	}

	moveHead(sideID)
	m = r.waitFor("the head on the side branch again", func(m view.Model) bool { return m.Leaf == sideID })
	if len(m.Rows) != 2 || len(assistantRows(m)) != 0 {
		t.Errorf("rows on the side branch = %s", describe(m))
	}
}
