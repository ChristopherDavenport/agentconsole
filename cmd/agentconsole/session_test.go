package main

import (
	"context"
	"testing"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"
	"github.com/ChristopherDavenport/openresponses/echo"
)

// roundTrip runs one prompt in the session the flags name, closes the
// store, and returns the session ID and the transcript the agent held.
func roundTrip(t *testing.T, kind, root, resume, conversation, prompt string) (string, int) {
	t.Helper()
	ctx := context.Background()
	st, closeStore, err := openStore(kind, root)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore()
	rec, seed, err := openSession(ctx, st, resume, conversation)
	if err != nil {
		t.Fatal(err)
	}
	ag := agentturn.New(agentturn.Config{Model: &echo.Adapter{}, ModelName: "echo"}, seed...)
	defer rec.Attach(ag)()
	before := len(ag.State().Transcript)
	if _, err := ag.Prompt(ctx, openresponses.UserText(prompt)); err != nil {
		t.Fatal(err)
	}
	return rec.SessionID(), before
}

func TestOpenSessionResumesByIDRefAndConversation(t *testing.T) {
	for _, kind := range []string{"jsonl", "cas"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			id, before := roundTrip(t, kind, root, "", "", "one")
			if before != 0 {
				t.Errorf("a new session starts with %d items", before)
			}
			id2, before := roundTrip(t, kind, root, id, "", "two")
			if id2 != id || before != 2 {
				t.Errorf("resume by ID: session %s (want %s) with %d items (want 2)", id2, id, before)
			}

			// A conversation name creates, then continues.
			cid, before := roundTrip(t, kind, root, "", "main", "a")
			if cid == id || before != 0 {
				t.Errorf("conversation main: session %s with %d items, want a new empty one", cid, before)
			}
			cid2, before := roundTrip(t, kind, root, "", "main", "b")
			if cid2 != cid || before != 2 {
				t.Errorf("conversation main again: session %s (want %s) with %d items (want 2)", cid2, cid, before)
			}
			// And the ref is a way to resume it.
			cid3, before := roundTrip(t, kind, root, "ref:main", "", "c")
			if cid3 != cid || before != 4 {
				t.Errorf("ref:main: session %s (want %s) with %d items (want 4)", cid3, cid, before)
			}
		})
	}
}

func TestOpenSessionErrors(t *testing.T) {
	ctx := context.Background()
	st, closeStore, err := openStore("jsonl", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore()
	if _, _, err := openSession(ctx, st, "ref:nope", ""); err == nil {
		t.Error("a ref that does not exist resumed")
	}
	if _, _, err := openSession(ctx, st, "x", "y"); err == nil {
		t.Error("--session with --conversation was accepted")
	}
	if _, _, err := openSession(ctx, st, "no-such-id", ""); err == nil {
		t.Error("an unknown session ID resumed")
	}
	if _, _, err := openStore("tape", t.TempDir()); err == nil {
		t.Error("an unknown store kind opened")
	}
	var _ agentsession.Store = st
}

// A session the client starts says who wrote it and where, in its own
// header: session.WithHarness names the writer only for child sessions.
func TestNewSessionsCarryTheHarnessAndTheDirectory(t *testing.T) {
	for _, conversation := range []string{"", "named"} {
		root := t.TempDir()
		id, _ := roundTrip(t, "jsonl", root, "", conversation, "hi")
		st, closeStore, err := openStore("jsonl", root)
		if err != nil {
			t.Fatal(err)
		}
		s, err := st.(agentsession.Reader).Read(context.Background(), id)
		closeStore()
		if err != nil {
			t.Fatal(err)
		}
		h := s.Header()
		if h.Harness == nil || h.Harness.Name != harnessName || h.Harness.Version != harnessVersion {
			t.Errorf("conversation %q: harness = %+v", conversation, h.Harness)
		}
		if h.CWD == "" {
			t.Errorf("conversation %q: no cwd in the header", conversation)
		}
	}
}
