package tui_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChristopherDavenport/agenttool"

	"github.com/ChristopherDavenport/agentconsole/internal/tui"
	"github.com/ChristopherDavenport/agentconsole/toolview"
	"github.com/ChristopherDavenport/agentconsole/view"
)

// upperRenderer draws a call of upper as the text it uppercases, and its
// output as what the text became. It knows the tool by its schema, which
// reaches it from the config on the record, and declines any other.
type upperRenderer struct{}

func (upperRenderer) args(c view.Call) (string, bool) {
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(c.Schema, &schema) != nil || schema.Properties["text"] == nil {
		return "", false
	}
	var a struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(c.Args), &a) != nil {
		return "", false
	}
	return a.Text, true
}

func (r upperRenderer) Head(c view.Call) (toolview.Line, bool) {
	text, ok := r.args(c)
	if !ok {
		return nil, false
	}
	return toolview.Line{toolview.S(toolview.Emphasis, "«"+text+"»")}, true
}

func (r upperRenderer) Body(c view.Call, _ bool) ([]toolview.Line, bool) {
	text, ok := r.args(c)
	if !ok || c.State != view.CallEnded {
		return nil, false
	}
	return []toolview.Line{{toolview.S(toolview.Dim, text+" became "+c.Output)}}, true
}

// A renderer draws a call from the stream, under a permission and from
// the record, with the client's notes and the raw arguments kept: the
// permission panel names the arguments as given, and ctrl+o shows them.
func TestAToolsRendererDrawsItsCallsLiveAndFromTheRecord(t *testing.T) {
	cfg := cfgWith(callTool("call_1", "upper", `{"text":"abc"}`), say(nil, nil, "done"))
	cfg.Tools = []agenttool.Tool{upperTool(nil)}
	cfg.BeforeToolCall = deferAll("may I run upper?")
	a := newAppWith(t, cfg, nil, nil, tui.WithToolRenderers(toolview.Renderers{"upper": upperRenderer{}}))

	a.submit("go")
	a.waitFor("the permission", has("Permission requested", `{"text":"abc"}`, "may I run upper?", "upper «abc» [deferred]"))

	a.typeText("y")
	s := a.waitFor("the run finished", all(has("upper «abc»", "abc became ABC", "done", "idle"), lacks("Permission requested", "(live)")))
	if strings.Contains(s, `text="abc"`) || strings.Contains(s, "1 line (ctrl+o)") {
		t.Errorf("the client's own head or body is drawn beside the renderer's:\n%s", s)
	}
	a.key("ctrl+o")
	a.waitFor("the call expanded", has("upper «abc» [ended]", `args: {"text":"abc"}`, "abc became ABC"))
}
