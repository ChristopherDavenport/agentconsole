package view

import (
	"strings"
	"testing"

	"github.com/ChristopherDavenport/agentsession"
)

func TestBranchesAreLabelledByTheirNewestMessage(t *testing.T) {
	l := newLog(t)
	root := l.append(itemEntry(msg("a", "user", "tell me about the weather in Lisbon today please and tomorrow"), ""))
	l.append(itemEntry(msg("b", "assistant", "sunny"), "r1"))
	side := itemEntry(msg("c", "user", "never mind"), "")
	side.Parent = root
	sid := l.append(side)

	m := l.v.Model()
	if len(m.Branches) != 2 {
		t.Fatalf("branches = %+v, want 2", m.Branches)
	}
	// Newest first, so the side branch leads; the viewed one is the
	// follower's leaf, the trunk.
	if m.Branches[0].Leaf != sid || m.Branches[0].Label != "user: never mind" || m.Branches[0].Role != "user" {
		t.Errorf("side branch = %+v", m.Branches[0])
	}
	if m.Branches[1].Label != "assistant: sunny" || !m.Branches[1].Current {
		t.Errorf("trunk branch = %+v", m.Branches[1])
	}
	if m.Branches[0].Current {
		t.Errorf("the side branch is not the viewed one: %+v", m.Branches[0])
	}
	// A long message is cut to its first words.
	l2 := newLog(t)
	l2.append(itemEntry(msg("a", "user", "tell me about the weather in Lisbon today please and tomorrow"), ""))
	if got := l2.v.Model().Branches[0].Label; got != "user: tell me about the weather in Lisbon today…" {
		t.Errorf("label = %q", got)
	}
}

func TestBranchLabelsCarryTheRunAndTheViewFollowsAHeadMove(t *testing.T) {
	l := newLog(t)
	l.append(agentsession.NewRunStart("run1", agentsession.SourceInput, ""))
	root := l.append(itemEntry(msg("a", "user", "q"), ""))
	l.append(itemEntry(msg("b", "assistant", "trunk"), "r"))
	side := itemEntry(msg("c", "user", "side"), "")
	side.Parent = root
	sid := l.append(side)
	if err := l.s.Branch(sid); err != nil {
		t.Fatal(err)
	}
	l.v.Record(agentsession.Change{Kind: agentsession.Head, Session: l.s, Leaf: sid})
	m := l.v.Model()
	for _, b := range m.Branches {
		if b.RunID != "run1" {
			t.Errorf("branch %+v has no run", b)
		}
		if b.Current != (b.Leaf == sid) {
			t.Errorf("current = %v for %s, the view is on %s", b.Current, b.Leaf, sid)
		}
		if b.Time.IsZero() {
			t.Errorf("branch %+v has no time", b)
		}
	}
}

func TestLinksAndOriginAreCollectedOnce(t *testing.T) {
	l := newLog(t)
	l.append(itemEntry(msg("a", "user", "q"), ""))
	id := l.append(agentsession.NewSubsessionLink("child1", "call_9"))
	l.append(agentsession.NewLinkEntry(agentsession.RelContinuedIn, "next1"))
	// The same entry delivered again (a replay) is not a second link.
	e, _ := l.s.Entry(id)
	l.v.Record(agentsession.Change{Kind: agentsession.Appended, Session: l.s, ID: id, Entry: e})

	m := l.v.Model()
	if len(m.Links) != 2 {
		t.Fatalf("links = %+v, want 2", m.Links)
	}
	if m.Links[0].Rel != agentsession.RelSubsession || m.Links[0].Session != "child1" || m.Links[0].CallID != "call_9" || m.Links[0].Entry != id {
		t.Errorf("subsession link = %+v", m.Links[0])
	}
	if m.Links[1].Rel != agentsession.RelContinuedIn || m.Links[1].Session != "next1" {
		t.Errorf("continued_in link = %+v", m.Links[1])
	}
	// A reset takes them out and the snapshot puts them back, once.
	l.v.Record(agentsession.Change{Kind: agentsession.Reset, Session: l.s})
	if got := len(l.v.Model().Links); got != 2 {
		t.Errorf("after a reset links = %d, want 2", got)
	}
}

func TestOriginComesFromTheHeader(t *testing.T) {
	origin := agentsession.New(agentsession.Header{ID: "origin1"})
	id, _ := origin.Append(itemEntry(msg("a", "user", "q"), ""))
	fork, err := agentsession.Fork(origin, id, agentsession.Header{ID: "fork1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fork.Append(itemEntry(msg("b", "assistant", "a"), "r")); err != nil {
		t.Fatal(err)
	}
	m := At(fork, "")
	if m.Origin.ParentSession != "origin1" || m.Origin.Base != id {
		t.Errorf("origin = %+v", m.Origin)
	}
	if m.Session != "fork1" || len(m.Rows) != 2 || m.Tail == "" || m.Entries != 2 {
		t.Errorf("fork view: session %q rows %d tail %q entries %d", m.Session, len(m.Rows), m.Tail, m.Entries)
	}
}

func TestAtShowsTheLineEndingAtTheLeafAndKeepsNothing(t *testing.T) {
	l := newLog(t)
	root := l.append(itemEntry(msg("a", "user", "root"), ""))
	l.append(itemEntry(msg("b", "assistant", "trunk"), "r"))
	side := itemEntry(msg("c", "user", "side"), "")
	side.Parent = root
	sid := l.append(side)

	m := At(l.s, sid)
	if m.Leaf != sid || len(m.Rows) != 2 || text(m.Rows[1].Item) != "side" {
		t.Fatalf("At(side) = leaf %s rows %+v", m.Leaf, m.Rows)
	}
	for _, r := range m.Rows {
		if r.Live {
			t.Errorf("a read-only view has no live rows: %+v", r)
		}
	}
	if m.Turn.State != Idle {
		t.Errorf("turn = %v, want idle", m.Turn.State)
	}
	if !strings.Contains(m.Branches[0].Label, "side") && !strings.Contains(m.Branches[1].Label, "side") {
		t.Errorf("branches = %+v", m.Branches)
	}
}
