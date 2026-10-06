package native_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentsession/jsonl"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client/native"
	"github.com/ChristopherDavenport/agentconsole/view"
)

// recording is a model that keeps the user messages of each request it
// is sent before the script answers it.
type recording struct {
	*script
	mu     sync.Mutex
	inputs [][]string
}

func (r *recording) CreateStream(ctx context.Context, req openresponses.Request, sink openresponses.EventSink) error {
	var in []string
	for _, item := range req.Input {
		if m, ok := item.(*openresponses.Message); ok && m.Role == openresponses.RoleUser {
			in = append(in, m.Text())
		}
	}
	r.mu.Lock()
	r.inputs = append(r.inputs, in)
	r.mu.Unlock()
	return r.script.CreateStream(ctx, req, sink)
}

// TestASteerLeftByAQuitIsDeliveredOnResume steers into a run, aborts it
// and lets the process go, so the agent's queue dies with it while the
// record still owes the steer. A backend over the resumed session lists
// it as queued and its next run takes it after the prompt, as the first
// process would have: the item names the queued entry written before the
// quit, and no other is written.
func TestASteerLeftByAQuitIsDeliveredOnResume(t *testing.T) {
	ctx := context.Background()
	store, err := jsonl.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// The first process: a run steered and then aborted.
	g := newGates()
	rec1, sess, err := session.Start(ctx, store, agentsession.Header{})
	if err != nil {
		t.Fatal(err)
	}
	a1 := agentturn.New(agentturn.Config{ModelName: "m", Model: &script{responses: []func(context.Context, *openresponses.Emitter) error{
		say(g, map[int]string{0: "mid"}, "partial", "never"),
	}}})
	detach1 := rec1.Attach(a1)
	be1, err := native.New(a1, rec1)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- be1.Control().Prompt(ctx, openresponses.UserText("start")) }()
	g.arrive(t, "mid")
	if err := be1.Control().Steer(ctx, openresponses.UserText("also this")); err != nil {
		t.Fatal(err)
	}
	be1.Control().Abort()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("aborted run: %v", err)
	}
	g.release("mid")
	detach1()

	// The second process: the session resumed, a new agent seeded from it.
	rec2, sess2, err := session.Resume(ctx, store, sess.ID())
	if err != nil {
		t.Fatal(err)
	}
	owed, err := sess2.PendingQueued(sess2.Leaf())
	if err != nil || len(owed) != 1 || text(owed[0].Item) != "also this" {
		t.Fatalf("owed at resume: %v %+v", err, owed)
	}
	seed, err := session.AgentOptions(sess2, rec2.ReadOptions()...)
	if err != nil {
		t.Fatal(err)
	}
	model := &recording{script: &script{responses: []func(context.Context, *openresponses.Emitter) error{say(nil, nil, "next answer")}}}
	a2 := agentturn.New(agentturn.Config{ModelName: "m", Model: model}, seed...)
	detach2 := rec2.Attach(a2)
	defer detach2()
	be2, err := native.New(a2, rec2)
	if err != nil {
		t.Fatal(err)
	}
	if st := a2.State(); st.Steering != 1 || text(st.Steered[0]) != "also this" {
		t.Fatalf("the resumed agent holds %d steers (%v), want the one owed", st.Steering, st.Steered)
	}
	if m := modelOf(t, be2); len(m.Queued) != 1 || m.Queued[0].EntryID != owed[0].ID {
		t.Fatalf("queued at resume = %+v, want the entry %s", m.Queued, owed[0].ID)
	}

	if err := be2.Control().Prompt(ctx, openresponses.UserText("go on")); err != nil {
		t.Fatal(err)
	}
	model.mu.Lock()
	in := model.inputs
	model.mu.Unlock()
	if len(in) != 1 || len(in[0]) < 2 || in[0][len(in[0])-2] != "go on" || in[0][len(in[0])-1] != "also this" {
		t.Errorf("the request's user messages = %q, want the prompt and then the steer", in)
	}
	s, err := be2.Record().Read(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var queuedEntries, drained int
	for _, e := range s.Path(s.Leaf()) {
		switch x := e.(type) {
		case *agentsession.QueuedEntry:
			queuedEntries++
		case *agentsession.ItemEntry:
			if x.QueuedFrom == owed[0].ID {
				drained++
			}
		}
	}
	if drained != 1 {
		t.Errorf("%d items name the owed entry, want 1", drained)
	}
	// One written when steered, one again after the aborted run's end.
	if queuedEntries != 2 {
		t.Errorf("%d queued entries on the path, want 2: the resume writes none", queuedEntries)
	}
	if m := modelOf(t, be2); len(m.Queued) != 0 || userRows(m, "also this") != 1 {
		t.Errorf("after the run: queued %+v, %d rows of the steer", m.Queued, userRows(m, "also this"))
	}
}

// modelOf renders the backend's session as a view attached now would.
func modelOf(t *testing.T, be *native.Backend) view.Model {
	t.Helper()
	s, err := be.Record().Read(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	v := view.New()
	v.Record(agentsession.Change{Kind: agentsession.Snapshot, Session: s})
	return v.Model()
}
