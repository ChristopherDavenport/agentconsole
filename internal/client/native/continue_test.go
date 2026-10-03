package native_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/internal/view"
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
	r.allowDrop = true

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
