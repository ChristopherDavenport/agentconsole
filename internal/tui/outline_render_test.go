package tui

import (
	"strings"
	"testing"

	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/view"
)

func quietRow(id, name string) view.Row {
	r := callRow("e_"+id, view.Call{CallID: id, Name: name, Args: `{}`, State: view.CallEnded, Committed: true})
	r.ResponseID = "resp_" + id
	return r
}

// A call still going is never drawn inside a group, however many quiet
// calls come before it, and the group's line says what ended.
func TestARunningCallStaysOutOfTheGroupLine(t *testing.T) {
	running := callRow("e_3", view.Call{CallID: "c3", Name: "bash", Args: `{"command":"sleep 9"}`, State: view.CallRunning})
	m := view.Model{Rows: []view.Row{quietRow("c1", "bash"), quietRow("c2", "bash"), running}}
	out, _ := renderRows(m, opts{}, nil, nil, 80, "", "", newMarkdown(), asks{}, grouping{})
	got := plain(out)
	if !strings.Contains(got, "ran 2 bash · turns 1–2") || !strings.Contains(got, "bash command=") || !strings.Contains(got, "[running]") {
		t.Errorf("the running call is not drawn beside the group:\n%s", got)
	}
	// Expanded, the members are drawn as rows.
	out, _ = renderRows(m, opts{}, nil, nil, 80, "", "", newMarkdown(), asks{}, grouping{open: map[string]bool{"e_c2": true}})
	if got := plain(out); !strings.Contains(got, "ran 2 bash") || strings.Count(got, "○ bash") != 2 {
		t.Errorf("expanded:\n%s", got)
	}
}

func assistantRow(id, resp, text string) view.Row {
	m := openresponses.UserText(text)
	m.Role = openresponses.RoleAssistant
	return view.Row{EntryID: id, ResponseID: resp, Item: m}
}

// A turn a group cuts in two is drawn with one header, and the turn a
// collapsed group swallows keeps one only where its own rows go on.
func TestATurnCutByAGroupHasOneHeader(t *testing.T) {
	c1, c2, c3 := quietRow("c1", "bash"), quietRow("c2", "bash"), quietRow("c3", "bash")
	c1.ResponseID, c2.ResponseID, c3.ResponseID = "r1", "r1", "r2"
	m := view.Model{Rows: []view.Row{assistantRow("m1", "r1", "starting"), c1, c2, c3, assistantRow("m2", "r2", "finished")}}
	out, _ := renderRows(m, opts{}, nil, nil, 80, "", "", newMarkdown(), asks{}, grouping{})
	got := plain(out)
	if n := strings.Count(got, "turn 1 ·"); n != 1 {
		t.Errorf("turn 1 has %d headers:\n%s", n, got)
	}
	if n := strings.Count(got, "turn 2 ·"); n != 1 || strings.Index(got, "turn 2 ·") < strings.Index(got, "ran 3 bash") {
		t.Errorf("turn 2's header is not once, after the group's line:\n%s", got)
	}
	if !strings.Contains(got, "turn 1 · 2 calls") {
		t.Errorf("turn 1's counts are not the whole turn's:\n%s", got)
	}
	// Expanded, the headers are between the members.
	out, _ = renderRows(m, opts{}, nil, nil, 80, "", "", newMarkdown(), asks{}, grouping{open: map[string]bool{"e_c1": true}})
	got = plain(out)
	if strings.Count(got, "turn 1 ·") != 1 || strings.Count(got, "turn 2 ·") != 1 {
		t.Errorf("expanded:\n%s", got)
	}
}
