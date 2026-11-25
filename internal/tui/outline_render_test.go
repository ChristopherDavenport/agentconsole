package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/view"
)

func quietRow(id, name, resp string) view.Row {
	r := callRow("e_"+id, view.Call{CallID: id, Name: name, Args: `{}`, State: view.CallEnded, Committed: true})
	r.ResponseID = resp
	return r
}

func assistantRow(id, resp, text string) view.Row {
	m := openresponses.UserText(text)
	m.Role = openresponses.RoleAssistant
	return view.Row{EntryID: id, ResponseID: resp, Item: m}
}

// A call still going is never drawn inside a summary, and its turn is
// not in a run, however many quiet turns come before it.
func TestARunningCallIsNotSummarized(t *testing.T) {
	running := callRow("e_3", view.Call{CallID: "c3", Name: "bash", Args: `{"command":"sleep 9"}`, State: view.CallRunning})
	running.ResponseID = "r3"
	m := view.Model{Rows: []view.Row{quietRow("c1", "bash", "r1"), quietRow("c2", "bash", "r2"), running}}
	out, _ := renderRows(m, opts{}, nil, nil, 80, "", "", newMarkdown(), asks{}, grouping{})
	got := plain(out)
	for _, want := range []string{"turns 1–2 · ran 2 bash", "turn 3 · 1 call", "bash command=", "[running]"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	// Expanded, the run shows its turns, each a header and its summary.
	out, _ = renderRows(m, opts{}, nil, nil, 80, "", "", newMarkdown(), asks{}, grouping{runs: map[string]bool{"r1": true}})
	got = plain(out)
	if strings.Count(got, "ran 1 bash") != 2 || !strings.Contains(got, "turn 1 · 1 call") || !strings.Contains(got, "turn 2 · 1 call") {
		t.Errorf("expanded:\n%s", got)
	}
}

// A header is one line of text with no gap under it, and never wraps.
func TestATurnHeaderIsOneLineWithNoGapUnderIt(t *testing.T) {
	ti := &turnInfo{calls: 2, outputs: 2, failed: 1}
	if l := plain(turnLine(3, ti, 60)); l != "turn 3 · 2 calls → 2 outputs · 1 failed" {
		t.Errorf("header = %q", l)
	}
	for _, w := range []int{40, 30, 12, 8} {
		l := plain(turnLine(3, ti, w))
		if strings.Contains(l, "\n") || lipgloss.Width(l) > w {
			t.Errorf("width %d: %q wraps or overflows", w, l)
		}
	}
	ti.folded = true
	if l := plain(turnLine(3, ti, 80)); l != "▸ turn 3 · 2 calls → 2 outputs · 1 failed (ctrl+o to expand)" {
		t.Errorf("folded = %q", l)
	}

	m := view.Model{Rows: []view.Row{assistantRow("m1", "r1", "hello"), assistantRow("m2", "r2", "bye")}}
	out, spans := renderRows(m, opts{}, nil, nil, 40, "", "", newMarkdown(), asks{}, grouping{})
	lines := strings.Split(plain(out), "\n")
	if !strings.HasPrefix(lines[0], "turn 1 · answer") || !strings.HasPrefix(lines[1], "agent") {
		t.Errorf("a gap under the header:\n%q", lines)
	}
	for _, sp := range spans {
		if !strings.HasPrefix(lines[sp.start], map[string]string{"m1": "agent", "m2": "agent", "turn:r1": "turn 1", "turn:r2": "turn 2"}[sp.entry]) {
			t.Errorf("span %+v does not start on its block: %q", sp, lines[sp.start])
		}
	}
	// A folded header has nothing under it: the next header is a block of
	// its own, a blank line below.
	out, spans = renderRows(m, opts{}, nil, nil, 40, "", "", newMarkdown(), asks{}, grouping{folded: map[string]bool{"r1": true}})
	lines = strings.Split(plain(out), "\n")
	if !strings.HasPrefix(lines[0], "▸ turn 1") || strings.TrimSpace(lines[1]) != "" || !strings.HasPrefix(lines[2], "turn 2") {
		t.Errorf("folded then header:\n%q", lines)
	}
	for _, sp := range spans {
		if sp.entry == "turn:r2" && sp.start != 2 {
			t.Errorf("turn 2's span starts at %d, want 2", sp.start)
		}
	}
}
