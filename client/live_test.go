package client

import (
	"context"
	"testing"
	"time"

	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"
	"github.com/ChristopherDavenport/openresponses/echo"
)

// TestCopySharesNothingWithTheAgent pins the rule the feed depends on:
// the agent's item keeps changing after the event, and the copy must
// not.
func TestCopySharesNothingWithTheAgent(t *testing.T) {
	m := openresponses.UserText("a")
	m.ID = "m1"
	ev, ok := Copy(&agentturn.ItemUpdate{RunID: "r", Item: m, ResponseID: "resp"})
	if !ok {
		t.Fatal("item_update dropped")
	}
	m.Content[0] = &openresponses.OutputText{Text: "changed"}
	if got := ev.(*agentturn.ItemUpdate).Item.(*openresponses.Message).Text(); got != "a" {
		t.Errorf("the copied item changed with the agent's: %q", got)
	}

	call := &openresponses.FunctionCall{CallID: "c1", Name: "rm", Arguments: "{}"}
	d := &agentturn.ToolDecision{Action: agentturn.Defer, Reason: "ask", Held: true, Subject: "rm -rf x"}
	ev, _ = Copy(&agentturn.RunEnd{RunID: "r", Reason: agentturn.ReasonInputRequired, Cause: "", Pending: []agentturn.PendingCall{
		{Call: call, Reason: agentturn.PendingDeferred, Dispatched: true, Decision: d},
	}})
	call.Arguments, d.Subject = "changed", "changed"
	p := ev.(*agentturn.RunEnd).Pending[0]
	if p.Call.Arguments != "{}" || p.Decision.Subject != "rm -rf x" || !p.Decision.Held || !p.Dispatched {
		t.Errorf("pending = %+v, decision %+v: the copy changed or lost the decision", p, p.Decision)
	}
}

func TestCopyLeavesOutWhatTheViewDoesNotRead(t *testing.T) {
	cases := []struct {
		name string
		ev   agentturn.Event
		keep bool
	}{
		{"hidden start", &agentturn.ItemStart{Item: openresponses.UserText("x"), Hidden: true}, false},
		{"hidden end", &agentturn.ItemEnd{Item: openresponses.UserText("x"), Hidden: true}, false},
		{"turn end", &agentturn.TurnEnd{}, false},
		{"queued", &agentturn.Queued{}, false},
		{"model blocked", &agentturn.ModelBlocked{}, false},
		{"response end without a response", &agentturn.ResponseEnd{}, false},
		{"run start", &agentturn.RunStart{RunID: "r"}, true},
		{"item end", &agentturn.ItemEnd{Item: openresponses.UserText("x")}, true},
		{"question", &agentturn.Question{ID: "q1"}, true},
		{"question closed", &agentturn.QuestionClosed{ID: "q1"}, true},
	}
	for _, tc := range cases {
		if _, ok := Copy(tc.ev); ok != tc.keep {
			t.Errorf("%s: kept = %v, want %v", tc.name, ok, tc.keep)
		}
	}
}

func TestCopyKeepsWhatTheViewReads(t *testing.T) {
	ev, _ := Copy(&agentturn.ResponseEnd{RunID: "r", Response: &openresponses.Response{ID: "resp", Model: "m",
		Usage: &openresponses.Usage{InputTokens: 3}, Output: openresponses.Items{openresponses.UserText("x")}}, Withheld: true})
	re := ev.(*agentturn.ResponseEnd)
	if !re.Withheld || re.Response.ID != "resp" || re.Response.Model != "m" || re.Response.Usage.InputTokens != 3 || len(re.Response.Output) != 0 {
		t.Errorf("response_end copy = %+v, response %+v", re, re.Response)
	}
	ev, _ = Copy(&agentturn.RunEnd{RunID: "r", Reason: agentturn.ReasonStopped, Cause: agentturn.StopMaxTurns, Withheld: true,
		Items: agentturn.Transcript{openresponses.UserText("x")}})
	end := ev.(*agentturn.RunEnd)
	if !end.Withheld || end.Cause != agentturn.StopMaxTurns || len(end.Items) != 0 {
		t.Errorf("run_end copy = %+v", end)
	}
	ev, _ = Copy(&agentturn.TurnStart{RunID: "r", Turn: 1, Request: openresponses.Request{Model: "m", Input: openresponses.Items{openresponses.UserText("x")}}})
	if ts := ev.(*agentturn.TurnStart); ts.Request.Model != "m" || len(ts.Request.Input) != 0 {
		t.Errorf("turn_start copy = %+v", ts)
	}
}

// TestLiveSubscribesWhenCalled: an event the agent raises after Live
// returns and before the caller ranges over it is not missed.
func TestLiveSubscribesWhenCalled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := agentturn.New(agentturn.Config{Model: &echo.Adapter{}, ModelName: "echo"})
	live := Live(ctx, a)
	if _, err := a.Prompt(ctx, openresponses.UserText("hi")); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool)
	go func() {
		for ev, err := range live {
			if err != nil {
				break
			}
			if _, ok := ev.(*agentturn.RunEnd); ok {
				done <- true
				return
			}
		}
		done <- false
	}()
	select {
	case ok := <-done:
		if !ok {
			t.Error("the stream ended before the run's end")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the run's end never came")
	}
}
