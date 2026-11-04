package tui_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"

	"github.com/ChristopherDavenport/agentconsole/internal/tui"
)

// runAtOnce runs an editor where the program would suspend for it.
func runAtOnce(c *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
	return func() tea.Msg { return fn(c.Run()) }
}

// Ctrl-G opens the input in the user's editor, $VISUAL or else $EDITOR
// split as a shell would, and loads what was saved back into the input
// without sending it. The file is private and gone afterwards (#46).
func TestCtrlGEditsTheInputInTheEditor(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	script := filepath.Join(t.TempDir(), "ed it.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n[ \"$1\" = --flag ] || exit 3\nstat -c %a \"$2\" > \""+tmp+"-mode\"\necho 'and an edited line' >> \"$2\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "'"+script+"' --flag")
	a := newAppWith(t, cfgWith(say(nil, nil, "hi")), nil, nil, tui.WithExec(runAtOnce))
	a.typeText("the draft")
	a.key("ctrl+g")
	s := a.waitFor("the edited input", has("the draft", "and an edited line"))
	if strings.Contains(s, "hi") {
		t.Errorf("the edited input was sent:\n%s", s)
	}
	if mode, err := os.ReadFile(tmp + "-mode"); err != nil || strings.TrimSpace(string(mode)) != "600" {
		t.Errorf("the file's mode %q (%v), want 600", mode, err)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("left behind in the temp dir: %v", left)
	}
}

// With no editor set, or one that fails, the status line says so and the
// input stays as it was.
func TestCtrlGWithoutAnEditorSaysSo(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	a := newAppWith(t, cfgWith(say(nil, nil, "hi")), nil, nil, tui.WithExec(runAtOnce))
	a.typeText("kept")
	a.key("ctrl+g")
	a.waitFor("the note", has("no editor", "kept"))

	t.Setenv("EDITOR", "false")
	a.key("ctrl+g")
	s := a.waitFor("the failure", has("editor", "exit status 1"))
	if !strings.Contains(lineWith(s, "> "), "kept") {
		t.Errorf("the input changed:\n%s", s)
	}
}

// What was in the input when a permission came is kept through its
// refusal: the reason is typed in the input, and the draft (a prompt
// composed in the editor, say) comes back after.
func TestADraftSurvivesARefusal(t *testing.T) {
	release := make(chan struct{})
	cfg := cfgWith(callTool("call_1", "upper", `{"text":"abc"}`), say(nil, nil, "done"))
	cfg.Tools = []agenttool.Tool{upperTool(nil)}
	cfg.BeforeToolCall = func(ctx context.Context, _ agentturn.ToolCallInfo) (*agentturn.ToolDecision, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return &agentturn.ToolDecision{Action: agentturn.Defer, Reason: "may I?"}, nil
	}
	a := newApp(t, cfg)
	a.submit("go")
	a.typeText("a long composed draft")
	close(release)
	a.waitFor("the permission", has("Permission requested"))
	a.rune('n')
	a.typeText("not now")
	a.key("enter")
	s := a.waitFor("the draft back", func(s string) bool { return strings.Contains(inputLine(s), "a long composed draft") })
	if strings.Contains(inputLine(s), "not now") {
		t.Errorf("the reason was left in the input:\n%s", s)
	}
}

// A refusal of an intent that lands while a refusal's reason is being
// typed ends that reason as Esc would: the half-typed reason goes and
// the draft the input held comes back.
func TestAnIntentRefusalEndsAReasonBeingTyped(t *testing.T) {
	release := make(chan struct{})
	cfg := cfgWith(callTool("call_1", "upper", `{"text":"abc"}`), say(nil, nil, "done"))
	cfg.Tools = []agenttool.Tool{upperTool(nil)}
	cfg.BeforeToolCall = func(ctx context.Context, _ agentturn.ToolCallInfo) (*agentturn.ToolDecision, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return &agentturn.ToolDecision{Action: agentturn.Defer, Reason: "may I?"}, nil
	}
	a := newApp(t, cfg)
	a.submit("go")
	a.typeText("my draft")
	close(release)
	a.waitFor("the permission", has("Permission requested"))
	a.rune('n')
	a.typeText("half a reason")
	a.send(tui.IntentDone("abort", errors.New("refused")))
	s := a.waitFor("the draft back", func(s string) bool { return strings.Contains(inputLine(s), "my draft") })
	if strings.Contains(s, "half a reason") {
		t.Errorf("the half-typed reason stayed:\n%s", s)
	}
}
