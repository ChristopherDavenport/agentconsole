package tui_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ChristopherDavenport/agenttool"
)

// Shift+Enter, and Alt+Enter and Ctrl+J for a terminal that sends it as
// Enter, break the input's line without sending it; Enter then sends both
// lines as one prompt.
func TestNewlineKeysBreakTheInputsLine(t *testing.T) {
	for _, key := range []string{"shift+enter", "alt+enter", "ctrl+j"} {
		t.Run(key, func(t *testing.T) {
			a := newApp(t, cfgWith(say(nil, nil, "hi")))
			a.typeText("first")
			a.key(key)
			a.typeText("second")
			s := a.screen()
			if strings.Contains(s, "hi") {
				t.Fatalf("%s sent the prompt:\n%s", key, s)
			}
			lines := strings.Split(s, "\n")
			row := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "first") })
			if row == -1 || row+1 >= len(lines) || strings.Contains(lines[row], "second") ||
				!strings.Contains(lines[row+1], "second") {
				t.Fatalf("%s did not put second on the row after first:\n%s", key, s)
			}
			a.key("enter")
			s = a.waitFor("the reply", all(has("hi", "idle"), func(s string) bool {
				return strings.Contains(lineWith(s, "> "), "say something")
			}))
			if strings.Count(s, "first") != 1 || strings.Count(s, "second") != 1 {
				t.Fatalf("the two lines were not sent as one prompt:\n%s", s)
			}
		})
	}
}

// inputRows is how many rows the input shows before it scrolls.
const inputRows = 5

// A typed line break is not held to the rows the input shows: the value
// grows past them and scrolls, as a paste does.
func TestNewlinesGoPastTheInputsRows(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "hi")))
	for i := range inputRows + 3 {
		if i > 0 {
			a.key("shift+enter")
		}
		a.typeText(fmt.Sprintf("line%d", i))
	}
	last := fmt.Sprintf("line%d", inputRows+2)
	if s := a.screen(); !strings.Contains(s, last) || strings.Contains(s, "line0") {
		t.Fatalf("the input did not grow past %d rows and scroll to %s:\n%s", inputRows, last, s)
	}
}

// A refusal's reason takes a line break too, rather than refusing.
func TestANewlineInARefusalsReason(t *testing.T) {
	cfg := cfgWith(callTool("call_1", "upper", `{"text":"abc"}`))
	cfg.Tools = []agenttool.Tool{upperTool(nil)}
	cfg.BeforeToolCall = deferAll("")
	a := newApp(t, cfg)
	a.submit("go")
	a.waitFor("the permission", has("Permission requested"))
	a.typeText("n")
	a.waitFor("the reason prompt", has("Reason for refusing"))
	a.typeText("too")
	a.key("shift+enter")
	a.typeText("risky")
	if s := a.screen(); !strings.Contains(s, "Permission requested") {
		t.Fatalf("shift+enter refused:\n%s", s)
	}
	a.key("enter")
	a.waitFor("the refusal recorded", all(has(`upper text="abc"`, "idle"), lacks("Permission requested")))
	a.key("ctrl+o")
	a.waitFor("the reason's two lines", has("Reason: too", "risky"))
}
