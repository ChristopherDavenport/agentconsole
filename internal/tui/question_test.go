package tui_test

import (
	"context"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client"
	"github.com/ChristopherDavenport/agentconsole/client/native"
)

// delegate is a tool whose call asks the user from inside it, as a
// sub-agent's call its policy holds does, and says what it was told.
func delegate(be *atomic.Pointer[native.Backend], call bool) agenttool.Tool {
	return agenttool.New("task", "delegates", func(ctx context.Context, _ textArgs) (string, error) {
		q := client.Question{Text: "a tool asks: continue?"}
		if call {
			q = client.Question{
				Call: &openresponses.FunctionCall{CallID: "child_1", Name: "write", Arguments: `{"path":"notes.md"}`},
				Text: "the task sub-agent asks: no rule allows write",
			}
		}
		r, err := be.Load().Ask(ctx, q)
		if err != nil {
			return "", err
		}
		if !r.Accept {
			return "SUBAGENT REFUSED " + r.Note, nil
		}
		return "SUBAGENT WROTE", nil
	})
}

func questionApp(t *testing.T, call bool) *app {
	var be atomic.Pointer[native.Backend]
	cfg := cfgWith(callTool("call_1", "task", `{"text":"go"}`), say(nil, nil, "done"))
	cfg.Tools = []agenttool.Tool{delegate(&be, call)}
	a := newApp(t, cfg)
	be.Store(a.be)
	return a
}

func TestASubagentsQuestionIsAskedWhileTheRunGoes(t *testing.T) {
	a := questionApp(t, true)
	a.submit("go")
	a.waitFor("the question", has("Question (1/1): write", `notes.md`, "the task sub-agent asks: no rule allows write", "[y] allow", "[n] refuse", "running"))
	a.typeText("y")
	a.waitFor("the run finished", all(has(`task text="go"`, `1 line (ctrl+o)`, "done", "idle"), lacks("Question (1/1)")))
}

func TestRefusingASubagentsQuestionSendsTheReason(t *testing.T) {
	a := questionApp(t, true)
	a.submit("go")
	a.waitFor("the question", has("Question (1/1)"))
	a.typeText("n")
	a.waitFor("the reason prompt", has("Reason for refusing"))
	a.key(tea.KeyEsc)
	a.waitFor("back at the question", has("[y] allow"))
	a.typeText("n")
	a.typeText("wrong file")
	a.key(tea.KeyEnter)
	a.waitFor("the refusal reached the call", all(has(`task text="go"`, "done", "idle"), lacks("Question (1/1)")))
	a.key(tea.KeyCtrlO)
	a.waitFor("the refusal's text", has("SUBAGENT REFUSED wrong file"))
}

func TestAToolsOwnQuestionIsYesOrNo(t *testing.T) {
	a := questionApp(t, false)
	a.submit("go")
	a.waitFor("the question", all(has("Question (1/1)", "a tool asks: continue?", "[y] yes", "[n] no"), lacks("[y] allow")))
	a.typeText("y")
	a.waitFor("the run finished", all(has(`task text="go"`, `1 line (ctrl+o)`, "idle"), lacks("Question (1/1)")))
}
