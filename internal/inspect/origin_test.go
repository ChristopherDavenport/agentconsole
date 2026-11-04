package inspect_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client"
	"github.com/ChristopherDavenport/agentconsole/internal/inspect"
)

// hiding is a source that does not hold one session.
type hiding struct {
	client.Record
	hidden string
}

func (h hiding) Read(ctx context.Context, id string) (*agentsession.Session, error) {
	if id == h.hidden {
		return nil, agentsession.ErrNoSession
	}
	return h.Record.Read(ctx, id)
}

// A call in a fork's prefix has its dispatch in the session the fork was
// made from, which the fork's file does not carry. The call's detail in
// the fork reads it there (agentsession.OriginDispatches) and says it is
// off this path and may have run, with its key (#5).
func TestAPrefixCallShowsItsOriginsDispatch(t *testing.T) {
	r := newRig(t, agentturn.Config{ModelName: "m", Tools: []agenttool.Tool{upper()},
		Model: &script{responses: []step{callTool("call_1", "upper", `{"text":"abc"}`), say("done")}}})
	r.prompt("go")
	origin, err := r.recd.Read(r.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var callEntry string
	for _, e := range origin.Entries() {
		if it, ok := e.(*agentsession.ItemEntry); ok {
			if _, isCall := it.Item.(*openresponses.FunctionCall); isCall {
				callEntry = it.ID
			}
		}
	}
	ds := origin.Dispatches(callEntry)
	if callEntry == "" || len(ds) == 0 {
		t.Fatalf("the origin holds no dispatched call")
	}
	fork, err := r.store.Create(r.ctx, agentsession.Header{ParentSession: origin.ID(), Base: callEntry, Records: agentsession.AllRecords})
	if err != nil {
		t.Fatal(err)
	}
	read := func(src inspect.Source) inspect.Entry {
		t.Helper()
		f, err := r.recd.Read(r.ctx, fork.ID())
		if err != nil {
			t.Fatal(err)
		}
		e, err := inspect.New(src).Entry(r.ctx, fork.ID(), f.Leaf(), callEntry, f.Len())
		if err != nil {
			t.Fatal(err)
		}
		return e
	}

	e := read(r.recd)
	if e.Call == nil || e.Call.OriginDispatch == nil || e.Call.OriginDispatch.Session != origin.ID() {
		t.Fatalf("the call's origin dispatch: %+v", e.Call)
	}
	text := joined(e.Lines())
	for _, want := range []string{"dispatched in origin " + origin.ID(), "off this path: may have run"} {
		if !strings.Contains(text, want) {
			t.Errorf("the pane does not say %q:\n%s", want, text)
		}
	}
	if k := ds[0].IdempotencyKey; k != "" && !strings.Contains(text, k) {
		t.Errorf("the pane does not show the key %s:\n%s", k, text)
	}

	// A store that does not hold the origin: no dispatch, and no error.
	e = read(hiding{Record: r.recd, hidden: origin.ID()})
	if e.Call.OriginDispatch != nil || e.Call.OriginErr != "" {
		t.Errorf("without the origin: %+v", e.Call)
	}
	if text := joined(e.Lines()); !strings.Contains(text, "dispatch: none recorded") {
		t.Errorf("without the origin the pane says:\n%s", text)
	}
}
