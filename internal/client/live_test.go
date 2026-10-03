package client

import (
	"testing"

	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"
)

// TestFromEventCopiesTheAccumulator pins the rule the native backend
// depends on: the agent's item keeps changing after the event, and the
// live event must not.
func TestFromEventCopiesTheAccumulator(t *testing.T) {
	m := openresponses.UserText("a")
	m.ID = "m1"
	ev, ok := FromEvent(&agentturn.ItemUpdate{RunID: "r", Item: m, ResponseID: "resp"})
	if !ok {
		t.Fatal("item_update dropped")
	}
	m.Content[0] = &openresponses.OutputText{Text: "changed"}
	got := ev.(*ItemUpdated).Item.(*openresponses.Message).Text()
	if got != "a" {
		t.Errorf("the live item changed with the agent's: %q", got)
	}
}

func TestFromEventNarrows(t *testing.T) {
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
	}
	for _, tc := range cases {
		if _, ok := FromEvent(tc.ev); ok != tc.keep {
			t.Errorf("%s: kept = %v, want %v", tc.name, ok, tc.keep)
		}
	}
}

func TestRunEndedListsPending(t *testing.T) {
	ev, _ := FromEvent(&agentturn.RunEnd{RunID: "r", Reason: agentturn.ReasonInputRequired, Pending: []agentturn.PendingCall{
		{Call: &openresponses.FunctionCall{CallID: "c1", Name: "rm", Arguments: "{}"}, Reason: agentturn.PendingDeferred},
	}})
	end := ev.(*RunEnded)
	if len(end.Pending) != 1 || end.Pending[0].CallID != "c1" || end.Pending[0].Reason != agentturn.PendingDeferred {
		t.Errorf("pending = %+v", end.Pending)
	}
}

func TestFromEventCarriesWithheld(t *testing.T) {
	ev, _ := FromEvent(&agentturn.ResponseEnd{RunID: "r", Response: &openresponses.Response{ID: "resp"}, Withheld: true})
	if !ev.(*ResponseCompleted).Withheld {
		t.Error("response_end lost Withheld")
	}
	ev, _ = FromEvent(&agentturn.RunEnd{RunID: "r", Withheld: true})
	if !ev.(*RunEnded).Withheld {
		t.Error("run_end lost Withheld")
	}
}
