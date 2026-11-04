package tui_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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
