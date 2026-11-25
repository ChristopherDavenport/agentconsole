package tui

import (
	"strings"
	"testing"

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
	if !strings.Contains(got, "ran 2 bash · 2 turns") || !strings.Contains(got, "bash command=") || !strings.Contains(got, "[running]") {
		t.Errorf("the running call is not drawn beside the group:\n%s", got)
	}
	// Expanded, the members are drawn as rows.
	out, _ = renderRows(m, opts{}, nil, nil, 80, "", "", newMarkdown(), asks{}, grouping{open: map[string]bool{"e_c2": true}})
	if got := plain(out); !strings.Contains(got, "ran 2 bash") || strings.Count(got, "○ bash") != 2 {
		t.Errorf("expanded:\n%s", got)
	}
}
