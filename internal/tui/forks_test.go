package tui_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ChristopherDavenport/agentkit"
	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"

	"github.com/ChristopherDavenport/agentconsole/client"
)

// bareRecord is a record with nothing beyond client.Record: no listing.
type bareRecord struct{ client.Record }

// forkedTwice is a session with an answer, forked twice at its entries,
// and a subsession spawned by the host under it.
func forkedTwice(t *testing.T, wrapRecord func(client.Record) client.Record) (*app, []string, []string) {
	t.Helper()
	cfg := cfgWith(say(nil, nil, "first answer"), say(nil, nil, "second answer"))
	a := newAppRecord(t, nil, func(ctx context.Context, rec *session.Recorder) (*agentkit.Kit, *agentturn.Agent, error) {
		kit, err := agentkit.New(ctx, agentkit.WithModel(cfg.Model, cfg.ModelName), agentkit.WithRecorder(rec), agentkit.WithQuestionEvents())
		if err != nil {
			return nil, nil, err
		}
		return kit, agentturn.New(cfg), nil
	}, wrapRecord, nil)
	a.submit("one")
	a.waitFor("the first answer", has("first answer", "idle"))
	a.submit("two")
	a.waitFor("the second answer", has("second answer", "idle"))
	s, err := a.record.Read(a.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var bases []string
	for _, e := range s.Path(s.Leaf()) {
		if _, ok := e.(*agentsession.ItemEntry); ok && len(bases) < 2 {
			bases = append(bases, e.Base().ID)
		}
	}
	var forks []string
	for _, base := range bases {
		f, err := a.store.Create(a.ctx, agentsession.Header{ParentSession: s.ID(), Base: base, Records: agentsession.AllRecords})
		if err != nil {
			t.Fatal(err)
		}
		forks = append(forks, f.ID())
	}
	// A child the host spawned names the session as its parent too, and
	// is not a fork.
	if _, err := a.store.Create(a.ctx, agentsession.Header{ParentSession: s.ID(), SpawnedBy: "call_x", Records: agentsession.AllRecords}); err != nil {
		t.Fatal(err)
	}
	return a, forks, bases
}

// The tree lists the sessions forked from this one, each with the entry
// it was forked at, read through the record's optional client.Lister; a
// child spawned from the session is not listed as a fork (#6).
func TestTheTreeListsTheSessionsForkedFromThisOne(t *testing.T) {
	a, forks, bases := forkedTwice(t, nil)
	a.key("ctrl+t")
	s := a.waitFor("the forks", has("sessions made from this one", "session "+forks[0]+" (made at", "session "+forks[1]+" (made at"))
	for i, f := range forks {
		if l := lineWith(s, f); !strings.Contains(l, entryID(bases[i])) {
			t.Errorf("fork %s does not name its base %s: %q", f, bases[i], l)
		}
	}
	if n := strings.Count(s, "(made at entry"); n != 2 {
		t.Errorf("%d forks listed, want 2:\n%s", n, s)
	}
}

// A record without the capability lists no forks, and the tree still
// shows the rest.
func TestARecordThatCannotListShowsNoForks(t *testing.T) {
	a, _, _ := forkedTwice(t, func(r client.Record) client.Record { return bareRecord{r} })
	a.key("ctrl+t")
	s := a.waitFor("the tree", has("branches of session"))
	if strings.Contains(s, "made from this one") {
		t.Errorf("forks listed without a lister:\n%s", s)
	}
}

// entryID is an entry's ID as the tree shortens it.
func entryID(id string) string { return strings.TrimPrefix(id, "sha256:")[:8] }
