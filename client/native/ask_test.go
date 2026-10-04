package native_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client"
	"github.com/ChristopherDavenport/agentconsole/client/native"
	"github.com/ChristopherDavenport/agentconsole/view"
)

// askTool asks the user before it answers, as a sub-agent's policy or a
// tool's elicitor would from inside a running call.
func askTool(be *atomic.Pointer[native.Backend], got chan<- client.Reply) agenttool.Tool {
	return agenttool.New("careful", "asks first", func(ctx context.Context, a struct {
		Text string `json:"text"`
	}) (string, error) {
		r, err := be.Load().Ask(ctx, client.Question{
			Call: &openresponses.FunctionCall{CallID: "child_1", Name: "write", Arguments: `{"path":"x"}`},
			Text: "the task sub-agent asks: may I write x?",
		})
		if err != nil {
			return "", err
		}
		got <- r
		if !r.Accept {
			return "refused: " + r.Note, nil
		}
		return "wrote " + a.Text, nil
	})
}

func askRig(t *testing.T) (*rig, chan client.Reply) {
	t.Helper()
	var be atomic.Pointer[native.Backend]
	got := make(chan client.Reply, 1)
	r := newRig(t, agentturn.Config{ModelName: "m", Tools: []agenttool.Tool{askTool(&be, got)}, Model: &script{responses: []func(context.Context, *openresponses.Emitter) error{
		callTool("call_1", "careful", `{"text":"x"}`),
		say(nil, nil, "done"),
	}}})
	be.Store(r.be)
	return r, got
}

func asking(m view.Model) bool { return len(m.Questions) == 1 }

func TestAQuestionIsAskedAndTheReplyReachesTheCall(t *testing.T) {
	r, got := askRig(t)
	run := r.prompt("go")
	m := r.waitFor("the question", asking)
	q := m.Questions[0]
	if q.ID == "" || q.Call == nil || q.Call.Name != "write" || q.Text != "the task sub-agent asks: may I write x?" {
		t.Fatalf("question %+v", q)
	}
	if m.Turn.State != view.Running {
		t.Errorf("the run is %v while its call waits, want running", m.Turn.State)
	}
	if err := r.ctl.Reply(q.ID, client.Reply{Accept: true}); err != nil {
		t.Fatal(err)
	}
	if rep := <-got; !rep.Accept {
		t.Errorf("the call got %+v", rep)
	}
	r.finish(run)
	r.waitFor("the question closed", func(m view.Model) bool { return len(m.Questions) == 0 })
	if err := r.ctl.Reply(q.ID, client.Reply{Accept: true}); err == nil {
		t.Error("a second reply to a closed question was taken")
	}
}

func TestARefusalCarriesTheNote(t *testing.T) {
	r, got := askRig(t)
	run := r.prompt("go")
	q := r.waitFor("the question", asking).Questions[0]
	if err := r.ctl.Reply(q.ID, client.Reply{Note: "not that file"}); err != nil {
		t.Fatal(err)
	}
	if rep := <-got; rep.Accept || rep.Note != "not that file" {
		t.Errorf("the call got %+v", rep)
	}
	r.finish(run)
}

// A client that attaches while a question waits is shown it: it is
// still waiting for someone to answer.
func TestALateSubscriberSeesTheWaitingQuestion(t *testing.T) {
	r, _ := askRig(t)
	run := r.prompt("go")
	q := r.waitFor("the question", asking).Questions[0]
	ctx, cancel := context.WithCancel(r.ctx)
	defer cancel()
	for ev, err := range r.be.Live(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		if e, ok := ev.(*client.QuestionAsked); ok {
			if e.Question.ID != q.ID {
				t.Errorf("late subscriber saw %+v", e.Question)
			}
			break
		}
		t.Fatalf("the late subscriber's first event is %T, want the waiting question", ev)
	}
	if err := r.ctl.Reply(q.ID, client.Reply{Accept: true}); err != nil {
		t.Fatal(err)
	}
	r.finish(run)
}

// An abort ends the run while its call waits: the question closes
// unanswered and the call gets the run's cancellation.
func TestAnAbortClosesTheQuestion(t *testing.T) {
	r, got := askRig(t)
	run := r.prompt("go")
	r.waitFor("the question", asking)
	r.ctl.Abort()
	r.waitFor("the question closed", func(m view.Model) bool { return len(m.Questions) == 0 })
	select {
	case err := <-run:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("run: %v", err)
		}
	case <-time.After(wait):
		t.Fatal("the run did not end")
	}
	select {
	case rep := <-got:
		t.Errorf("the call got a reply %+v after an abort", rep)
	default:
	}
}
