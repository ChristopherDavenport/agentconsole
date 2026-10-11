package tui_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
)

// delegate is a tool whose call asks the user from inside it: with call
// set, about a sub-agent's call its policy holds (agentturn.Ask, as a
// child agent's hook does), otherwise a question of its own through the
// elicitor on its context. It says what it was told. The kit's Control
// makes either a Question event the client answers with Reply.
func delegate(call bool) agenttool.Tool {
	return agenttool.New("task", "delegates", func(ctx context.Context, _ textArgs) (string, error) {
		if call {
			d, ok := agentturn.Ask(ctx, agentturn.AskedCall{Parent: "call_1", CallID: "child_1", Name: "write",
				Args:     json.RawMessage(`{"path":"notes.md"}`),
				Decision: &agentturn.ToolDecision{Action: agentturn.Defer, Reason: "the task sub-agent asks: no rule allows write", Subject: "notes.md"}})
			switch {
			case !ok:
				return "", errors.New("not asked")
			case d.Action == agentturn.Block:
				return "SUBAGENT REFUSED " + d.Reason, nil
			}
			return "SUBAGENT WROTE " + d.Note, nil
		}
		elicit, ok := agenttool.ElicitorFrom(ctx)
		if !ok {
			return "", errors.New("no elicitor")
		}
		ans, err := elicit(ctx, agenttool.Elicitation{Message: "a tool asks: continue?"})
		if err != nil {
			return "", err
		}
		return "TOOL HEARD " + string(ans.Action) + " " + ans.Note, nil
	})
}

func questionApp(t *testing.T, call bool) *app {
	cfg := cfgWith(callTool("call_1", "task", `{"text":"go"}`), say(nil, nil, "done"))
	cfg.Tools = []agenttool.Tool{delegate(call)}
	return newApp(t, cfg)
}

func TestASubagentsQuestionIsAskedWhileTheRunGoes(t *testing.T) {
	a := questionApp(t, true)
	a.submit("go")
	a.waitFor("the question", has("Question (1/1): write", `notes.md`, "the task sub-agent asks: no rule allows write", "[y] allow", "[a] allow with a note", "[n] refuse", "running"))
	a.typeText("y")
	a.waitFor("the run finished", all(has(`task text="go"`, `1 line (ctrl+o)`, "done", "idle"), lacks("Question (1/1)")))
	a.key("ctrl+o")
	a.waitFor("the call's output", has("SUBAGENT WROTE"))
}

func TestRefusingASubagentsQuestionSendsTheReason(t *testing.T) {
	a := questionApp(t, true)
	a.submit("go")
	a.waitFor("the question", has("Question (1/1)"))
	a.typeText("n")
	a.waitFor("the reason prompt", has("Reason for refusing"))
	a.key("esc")
	a.waitFor("back at the question", has("[y] allow"))
	a.typeText("n")
	a.typeText("wrong file")
	a.key("enter")
	a.waitFor("the refusal reached the call", all(has(`task text="go"`, "done", "idle"), lacks("Question (1/1)")))
	a.key("ctrl+o")
	a.waitFor("the refusal's text", has("SUBAGENT REFUSED declined when asked", "said: wrong file"))
}

// TestAllowingWithANoteSendsTheNote: the user's words with an allow reach
// the call, which Ask hands the model as the decision's note.
func TestAllowingWithANoteSendsTheNote(t *testing.T) {
	a := questionApp(t, true)
	a.submit("go")
	a.waitFor("the question", has("Question (1/1)"))
	a.typeText("a")
	a.waitFor("the note prompt", has("A note with the answer"))
	a.typeText("only the notes")
	a.key("enter")
	a.waitFor("the run finished", all(has(`task text="go"`, "done", "idle"), lacks("Question (1/1)")))
	a.key("ctrl+o")
	a.waitFor("the note reached the model", has("SUBAGENT WROTE", "only the notes"))
}

func TestAToolsOwnQuestionIsYesOrNo(t *testing.T) {
	a := questionApp(t, false)
	a.submit("go")
	a.waitFor("the question", all(has("Question (1/1)", "a tool asks: continue?", "[y] yes", "[n] no"), lacks("[y] allow")))
	a.typeText("y")
	a.waitFor("the run finished", all(has(`task text="go"`, `1 line (ctrl+o)`, "idle"), lacks("Question (1/1)")))
	a.key("ctrl+o")
	a.waitFor("the tool's answer", has("TOOL HEARD accept"))
}
