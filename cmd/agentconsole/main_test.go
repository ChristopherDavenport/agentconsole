package main

import (
	"context"
	"testing"
	"time"

	"github.com/ChristopherDavenport/agentkit"
	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"
	"github.com/ChristopherDavenport/openresponses/echo"

	"github.com/ChristopherDavenport/agentconsole/client"
)

// What the person types at this binary's terminal is recorded as their
// words: the controller it hands the console claims a trigger of kind
// human for a prompt and for a steer, which the view does not (#50).
func TestTypedInputIsRecordedAsThePersons(t *testing.T) {
	ctx := context.Background()
	st, closeStore, err := openStore("jsonl", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore()
	rec, seed, err := openSession(ctx, st, "", "")
	if err != nil {
		t.Fatal(err)
	}
	kit, err := agentkit.New(ctx, agentkit.WithModel(&echo.Adapter{}, "echo"), agentkit.WithRecorder(rec))
	if err != nil {
		t.Fatal(err)
	}
	defer kit.Close()
	ag := agentturn.New(kit.Config(), append(kit.AgentOptions(), seed...)...)
	defer rec.Attach(ag)()
	ctl, err := kit.Control(ag)
	if err != nil {
		t.Fatal(err)
	}
	act := typedAt(client.Direct(ctl), atTerminal)
	idle := func() {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); ag.State().Running; time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("the run did not end")
			}
		}
	}
	if err := act(ctx, client.Prompt{Items: []openresponses.Item{openresponses.UserText("typed prompt")}}); err != nil {
		t.Fatal(err)
	}
	idle()
	// A steer sent with no run going is a delivery the agent starts a
	// run for (agentturn's Accept).
	if err := act(ctx, client.Queue{Mode: agentturn.QueueSteer, Items: []openresponses.Item{openresponses.UserText("typed steer")}}); err != nil {
		t.Fatal(err)
	}
	words := map[string]agentsession.Words{}
	for deadline := time.Now().Add(10 * time.Second); len(words) < 2; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the steer was not taken: %v", words)
		}
		s, err := st.(agentsession.Reader).Read(ctx, rec.SessionID())
		if err != nil {
			t.Fatal(err)
		}
		clear(words)
		for _, e := range s.Path(s.Leaf()) {
			if it, ok := e.(*agentsession.ItemEntry); ok {
				if m, ok := it.Item.(*openresponses.Message); ok && m.Role == openresponses.RoleUser {
					words[m.Text()] = agentsession.WordsOf(it)
				}
			}
		}
	}
	idle()
	for _, text := range []string{"typed prompt", "typed steer"} {
		if words[text] != agentsession.WordsPerson {
			t.Errorf("%q reads %v, want the person's words", text, words[text])
		}
	}
}
