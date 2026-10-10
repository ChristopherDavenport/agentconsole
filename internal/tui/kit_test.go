package tui_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ChristopherDavenport/agentkit"
	"github.com/ChristopherDavenport/agentpolicy"
	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/front/control"
	"github.com/ChristopherDavenport/openresponses"
)

// callAll is a model response of several function calls, one batch.
func callAll(calls ...[3]string) step {
	return func(ctx context.Context, em *openresponses.Emitter) error {
		for _, c := range calls {
			if err := callTool(c[0], c[1], c[2])(ctx, em); err != nil {
				return err
			}
		}
		return nil
	}
}

type shArgs struct {
	Command string `json:"command"`
}

// shell is a tool that says what it was asked to run, and remembers it.
type shell struct {
	mu  sync.Mutex
	ran []string
}

func (sh *shell) tool() agenttool.Tool {
	return agenttool.New("sh", "runs a command", func(_ context.Context, a shArgs) (string, error) {
		sh.mu.Lock()
		defer sh.mu.Unlock()
		sh.ran = append(sh.ran, a.Command)
		return "ran " + a.Command, nil
	})
}

func (sh *shell) commands() []string {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return append([]string(nil), sh.ran...)
}

// shMatcher matches sh's command by prefix and splits a command on &&,
// as a product's shell splitter does, each part a subject.
func shMatcher() map[string]agentpolicy.ToolMatcher {
	return map[string]agentpolicy.ToolMatcher{"sh": {
		Match: agentpolicy.PrefixMatcher("command"),
		Subjects: func(_ context.Context, args json.RawMessage) ([]agentpolicy.Subject, error) {
			var a shArgs
			if err := json.Unmarshal(args, &a); err != nil {
				return nil, err
			}
			var out []agentpolicy.Subject
			for part := range strings.SplitSeq(a.Command, "&&") {
				part = strings.TrimSpace(part)
				b, _ := json.Marshal(shArgs{Command: part})
				out = append(out, agentpolicy.Subject{Args: b, Text: part})
			}
			return out, nil
		},
	}}
}

func policyRules(t *testing.T, s string) []agentpolicy.Rule {
	t.Helper()
	r, err := agentpolicy.ParseRules(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestThePanelAsksAboutTheCallThePolicyAskedAboutAlone: in a batch the
// policy asks about one call of, for one part of its compound command,
// and holds the other beside it (agentpolicy's hold-for-batch), the
// panel asks about the one call, says which part, and the approval runs
// both: the kit's Control releases the held call with the answer. Asking
// about the held call too would let a refusal override what the user's
// own rule allowed (agentconsole#9).
func TestThePanelAsksAboutTheCallThePolicyAskedAboutAlone(t *testing.T) {
	sh := &shell{}
	model := &script{responses: []step{
		callAll([3]string{"c1", "sh", `{"command":"ls && rm -rf build"}`}, [3]string{"c2", "sh", `{"command":"ls"}`}),
		say(nil, nil, "both done"),
	}}
	a := newKitApp(t, []agentkit.Option{
		agentkit.WithModel(model, "scripted"),
		agentkit.WithTools(sh.tool()),
		agentkit.WithPolicy(agentpolicy.Policy{
			Allow:   policyRules(t, "sh(ls:*)"),
			Ask:     policyRules(t, "sh(rm:*)"),
			Default: agentpolicy.Ask(),
		}, shMatcher()),
	}, nil)
	a.submit("go")
	s := a.waitFor("the permission", has("Permission requested (1/1): sh", "approval required by sh(rm:*)", "about: rm -rf build", "[y] approve"))
	if strings.Contains(s, `{"command":"ls"}`) {
		t.Errorf("the held call is asked about:\n%s", s)
	}
	a.typeText("y")
	a.waitFor("both ran", all(has("both done", "idle"), lacks("Permission requested")))
	if got := sh.commands(); len(got) != 2 {
		t.Errorf("ran %q, want both calls", got)
	}
}

// TestTheClientDrivesAnAgentOverTheWire: the client over agentturn's
// front/control, in httptest, against a kit's Control: a prompt, a
// permission answered, and a question a running call asks replied with a
// note, which reaches the call.
func TestTheClientDrivesAnAgentOverTheWire(t *testing.T) {
	model := &script{responses: []step{
		callTool("c1", "danger", `{}`),
		callTool("call_1", "task", `{"text":"go"}`),
		say(nil, nil, "all done"),
	}}
	danger := agenttool.New("danger", "needs approval", func(context.Context, struct{}) (string, error) { return "did it", nil })
	dial := func(c agentturn.Control) agentturn.Control {
		srv := httptest.NewServer(control.Handler(c, control.WithInsecureNoAuth()))
		t.Cleanup(srv.Close)
		cl, err := control.Dial(context.Background(), srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		return cl
	}
	a := newKitApp(t, []agentkit.Option{
		agentkit.WithModel(model, "scripted"),
		agentkit.WithTools(danger, delegate(true)),
		agentkit.WithPolicy(agentpolicy.Policy{Ask: policyRules(t, "danger"), Default: agentpolicy.Allow()}, nil),
	}, dial)
	a.submit("go")
	a.waitFor("the permission", has("Permission requested (1/1): danger", "[y] approve"))
	a.typeText("y")
	a.waitFor("the question", has("Question (1/1): write", "[a] allow with a note"))
	a.typeText("a")
	a.waitFor("the note prompt", has("A note with the answer"))
	a.typeText("only the notes")
	a.key("enter")
	a.waitFor("the run finished", all(has("all done", "idle"), lacks("Question (1/1)")))
	a.key("ctrl+o")
	a.waitFor("the note reached the call", has("SUBAGENT WROTE The user said: only the notes"))
}
