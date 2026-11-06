package client

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"
	"github.com/ChristopherDavenport/openresponses/echo"

	"github.com/ChristopherDavenport/agentconsole/internal/scripted"
)

// resuming keeps the answers a Resume was given and starts nothing.
type resuming struct {
	agentturn.Control
	got []agentturn.Answer
}

func newResuming() *resuming {
	return &resuming{Control: agentturn.New(agentturn.Config{Model: &echo.Adapter{}, ModelName: "echo"})}
}

var errKept = errors.New("kept")

func (c *resuming) Resume(_ context.Context, answers ...agentturn.Answer) (*agentturn.RunEnd, error) {
	c.got = answers
	return nil, errKept
}

// TestDirectSendsAPlainApprovalForAGrant: the panel's "always" reaches
// the controller as a grant with the host's rule, and Direct, which
// makes no grant, resumes with a plain approval by the person (#41).
func TestDirectSendsAPlainApprovalForAGrant(t *testing.T) {
	c := newResuming()
	in := Answer{Calls: []CallAnswer{{CallID: "c1", Verdict: VerdictApprove, Grant: GrantSession, Rule: "bash(go test:*)"}}}
	if err := Direct(c)(context.Background(), in); !errors.Is(err, errKept) {
		t.Fatalf("answer: %v", err)
	}
	if len(c.got) != 1 {
		t.Fatalf("resumed with %#v, want one answer", c.got)
	}
	a := c.got[0]
	if a.CallID != "c1" || a.Output != nil || a.Terminate || a.By != "human" || a.Reason != "" {
		t.Errorf("answer = %#v, want a plain approval of c1 by the human", a)
	}
}

// deferred is an agent whose every call waits for an answer, over a
// scripted model, and the run ends it reported.
type deferred struct {
	*agentturn.Agent
	model *scripted.Model
	mu    sync.Mutex
	ends  []agentturn.RunEnd
}

func newDeferred(t *testing.T, steps ...scripted.Step) *deferred {
	t.Helper()
	d := &deferred{model: scripted.New(steps...)}
	rm := agenttool.New("rm", "remove", func(context.Context, struct{}) (string, error) { return "removed", nil })
	d.Agent = agentturn.New(agentturn.Config{
		Model: d.model, ModelName: "scripted", Tools: []agenttool.Tool{rm},
		BeforeToolCall: func(context.Context, agentturn.ToolCallInfo) (*agentturn.ToolDecision, error) {
			return &agentturn.ToolDecision{Action: agentturn.Defer}, nil
		},
	})
	t.Cleanup(d.Subscribe(func(_ context.Context, ev agentturn.Event) error {
		if e, ok := ev.(*agentturn.RunEnd); ok {
			d.mu.Lock()
			d.ends = append(d.ends, *e)
			d.mu.Unlock()
		}
		return nil
	}))
	return d
}

func (d *deferred) lastEnd() agentturn.RunEnd {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.ends) == 0 {
		return agentturn.RunEnd{}
	}
	return d.ends[len(d.ends)-1]
}

// asked prompts through act and waits for the run to stop on the call.
func (d *deferred) asked(t *testing.T, act Controller) {
	t.Helper()
	if err := act(context.Background(), Prompt{Items: []openresponses.Item{openresponses.UserText("clean up")}}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	idleWithin(t, d.Agent)
	if e := d.lastEnd(); e.Reason != agentturn.ReasonInputRequired {
		t.Fatalf("run ended %q, want it waiting on the call", e.Reason)
	}
}

// outputTo is the text of the output to callID in the model's request.
func outputTo(req openresponses.Request, callID string) string {
	for _, it := range req.Input {
		if o, ok := it.(*openresponses.FunctionCallOutput); ok && o.CallID == callID {
			return o.Output.Text
		}
	}
	return ""
}

// TestARefusalGoesOnOrStops: refuse-and-continue leaves the run going
// and the model reads the person's reason; refuse-and-stop ends it with
// StopRefused (#42).
func TestARefusalGoesOnOrStops(t *testing.T) {
	t.Run("continue", func(t *testing.T) {
		d := newDeferred(t, scripted.Call("c1", "rm", `{}`), scripted.Say("using npm ci"))
		act := Direct(d.Agent)
		d.asked(t, act)
		if err := act(context.Background(), Answer{Calls: []CallAnswer{{CallID: "c1", Verdict: VerdictRefuse, Reason: "use npm ci"}}}); err != nil {
			t.Fatalf("answer: %v", err)
		}
		idleWithin(t, d.Agent)
		if e := d.lastEnd(); e.Reason != agentturn.ReasonDone {
			t.Errorf("run ended %q (%q), want it to go on and complete", e.Reason, e.Cause)
		}
		reqs := d.model.Requests()
		if len(reqs) != 2 {
			t.Fatalf("%d model calls, want the run to call the model again", len(reqs))
		}
		if out := outputTo(reqs[1], "c1"); !strings.Contains(out, "use npm ci") {
			t.Errorf("the model read %q, want the reason", out)
		}
	})
	t.Run("stop", func(t *testing.T) {
		d := newDeferred(t, scripted.Call("c1", "rm", `{}`))
		act := Direct(d.Agent)
		d.asked(t, act)
		if err := act(context.Background(), Answer{Calls: []CallAnswer{{CallID: "c1", Verdict: VerdictStop, Reason: "I will do it"}}}); err != nil {
			t.Fatalf("answer: %v", err)
		}
		idleWithin(t, d.Agent)
		if e := d.lastEnd(); e.Reason != agentturn.ReasonStopped || e.Cause != agentturn.StopRefused {
			t.Errorf("run ended %q (%q), want stopped, refused", e.Reason, e.Cause)
		}
		if n := len(d.model.Requests()); n != 1 {
			t.Errorf("%d model calls, want none after the refusal", n)
		}
	})
}

// TestAHostsRefusalTextReplacesTheDefault: a controller's own answers
// (WithAnswers) give the model the host's words, for either kind (#42).
func TestAHostsRefusalTextReplacesTheDefault(t *testing.T) {
	hosts := WithAnswers(func(a CallAnswer) agentturn.Answer {
		if a.Verdict == VerdictApprove {
			return AnswerOf(a)
		}
		out := openresponses.NewFunctionCallOutput(a.CallID, "Error: the user denied this call. "+a.Reason)
		if a.Verdict == VerdictStop {
			return agentturn.Refuse(out).WithBy("human").WithReason(a.Reason)
		}
		return agentturn.Output(out).WithBy("human").WithReason(a.Reason)
	})
	d := newDeferred(t, scripted.Call("c1", "rm", `{}`), scripted.Say("ok"))
	act := Direct(d.Agent, hosts)
	d.asked(t, act)
	if err := act(context.Background(), Answer{Calls: []CallAnswer{{CallID: "c1", Verdict: VerdictRefuse, Reason: "use npm ci"}}}); err != nil {
		t.Fatalf("answer: %v", err)
	}
	idleWithin(t, d.Agent)
	reqs := d.model.Requests()
	if len(reqs) != 2 || outputTo(reqs[1], "c1") != "Error: the user denied this call. use npm ci" {
		t.Fatalf("the model read %q, want the host's words", outputTo(reqs[len(reqs)-1], "c1"))
	}

	c := newResuming()
	_ = Direct(c, hosts)(context.Background(), Answer{Calls: []CallAnswer{{CallID: "c2", Verdict: VerdictStop}}})
	if len(c.got) != 1 || !c.got[0].Terminate || c.got[0].Output == nil || !strings.HasPrefix(c.got[0].Output.Output.Text, "Error: the user denied") {
		t.Errorf("a stop answered %#v, want the host's refusal that ends the run", c.got)
	}
}

// TestAnswerOfBuildsTheDefaultWords: Direct's own answers: an approval,
// and a refusal of either kind whose output says so with the reason,
// the reason also on the answer.
func TestAnswerOfBuildsTheDefaultWords(t *testing.T) {
	cont := AnswerOf(CallAnswer{CallID: "c", Verdict: VerdictRefuse, Reason: "no"})
	stop := AnswerOf(CallAnswer{CallID: "c", Verdict: VerdictStop})
	if cont.Terminate || cont.Output == nil || cont.Output.Output.Text != "The user refused this call. Reason: no" || cont.Reason != "no" || cont.By != "human" {
		t.Errorf("refuse and continue = %#v", cont)
	}
	if !stop.Terminate || stop.Output == nil || stop.Output.Output.Text != "The user refused this call." || stop.By != "human" {
		t.Errorf("refuse and stop = %#v", stop)
	}
}
