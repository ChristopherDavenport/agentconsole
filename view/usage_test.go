package view

import (
	"testing"
	"time"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/openresponses"
)

func TestLastRunIsTheLastStartAndWhatFollowsIt(t *testing.T) {
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	at := func(s int) agentsession.EntryBase {
		return agentsession.EntryBase{Timestamp: t0.Add(time.Duration(s) * time.Second)}
	}
	usage := func(in, out int) *openresponses.Usage {
		return &openresponses.Usage{InputTokens: in, OutputTokens: out, TotalTokens: in + out}
	}
	path := []agentsession.Entry{
		&agentsession.RunEntry{EntryBase: at(0), RunID: "r1", Phase: agentsession.RunStart},
		&agentsession.ResponseEntry{EntryBase: at(1), ResponseID: "a", Usage: usage(100, 10)},
		&agentsession.RunEntry{EntryBase: at(2), RunID: "r1", Phase: agentsession.RunEnd},
		&agentsession.RunEntry{EntryBase: at(10), RunID: "r2", Phase: agentsession.RunStart},
		&agentsession.ResponseEntry{EntryBase: at(12), ResponseID: "b", Usage: usage(300, 20)},
		&agentsession.ResponseEntry{EntryBase: at(15), ResponseID: "c", Usage: usage(400, 30)},
	}
	if got := lastRun(nil); got != (Run{}) {
		t.Errorf("no run on the line: %+v", got)
	}

	got := lastRun(path)
	if got.ID != "r2" || !got.Started.Equal(t0.Add(10*time.Second)) || !got.Ended.IsZero() {
		t.Errorf("the run going: %+v", got)
	}
	if got.Usage.InputTokens != 700 || got.Usage.OutputTokens != 50 {
		t.Errorf("the run going counts %d in, %d out, want 700 in, 50 out", got.Usage.InputTokens, got.Usage.OutputTokens)
	}

	path = append(path, &agentsession.RunEntry{EntryBase: at(19), RunID: "r2", Phase: agentsession.RunEnd})
	if got := lastRun(path); got.ID != "r2" || !got.Ended.Equal(t0.Add(19*time.Second)) || got.Usage.InputTokens != 700 {
		t.Errorf("the run ended: %+v", got)
	}
}

func TestWorkedSumsTheRunsThatEnded(t *testing.T) {
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	run := func(id, phase string, s int) *agentsession.RunEntry {
		return &agentsession.RunEntry{EntryBase: agentsession.EntryBase{Timestamp: t0.Add(time.Duration(s) * time.Second)}, RunID: id, Phase: phase}
	}
	path := []agentsession.Entry{
		run("r1", agentsession.RunStart, 0), run("r1", agentsession.RunEnd, 2),
		// A run whose process died: no end, and nothing counted.
		run("r2", agentsession.RunStart, 10),
		run("r3", agentsession.RunStart, 100), run("r3", agentsession.RunEnd, 165),
		// The run going.
		run("r4", agentsession.RunStart, 200),
	}
	if got, want := worked(path), 67*time.Second; got != want {
		t.Errorf("worked %v, want %v", got, want)
	}
	if got := worked(nil); got != 0 {
		t.Errorf("no runs worked %v", got)
	}
}
