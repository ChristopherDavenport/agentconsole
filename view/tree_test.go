package view

import (
	"strings"
	"testing"
	"time"

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
	if m.Branches[1].Label != "agent: sunny" || !m.Branches[1].Current {
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

// A head moved back to an entry that the old line continues past is marked
// with a leaf label, and the next run extends it. The label is childless,
// and counting it as a tip showed a phantom branch resting on the entry.
func TestLeafLabelWhereTheHeadWasMovedIsNotABranch(t *testing.T) {
	l := newLog(t)
	root := l.append(itemEntry(msg("a", "user", "one"), ""))
	r1 := l.append(itemEntry(msg("b", "assistant", "r1"), "r1"))
	l.append(itemEntry(msg("c", "user", "two"), ""))
	if err := l.s.Branch(r1); err != nil {
		t.Fatal(err)
	}
	mark, err := l.s.MarkLeaf()
	if err != nil {
		t.Fatal(err)
	}
	l.append(mark)

	// Nothing has been written on the new head yet: the old line's tip
	// and the head itself, one each.
	m := l.v.Model()
	if len(m.Branches) != 2 {
		t.Fatalf("branches = %+v, want the old line and the head", m.Branches)
	}
	if m.Leaf != r1 {
		t.Fatalf("leaf = %s, want the head %s", m.Leaf, r1)
	}
	var current int
	for _, b := range m.Branches {
		if b.Current {
			current++
			if b.Leaf != r1 {
				t.Errorf("the current branch rests on %s, want %s", b.Leaf, r1)
			}
		}
	}
	if current != 1 {
		t.Errorf("%d current branches, want 1", current)
	}

	// The run extends the head: the label stops being the head's only
	// mark and the branch is the new line.
	l.append(itemEntry(msg("d", "user", "three"), ""))
	m = l.v.Model()
	if len(m.Branches) != 2 {
		t.Fatalf("after the run extended the head: branches = %+v, want the old line and the new one", m.Branches)
	}
	for _, b := range m.Branches {
		if b.Leaf == r1 {
			t.Errorf("a phantom branch rests on the entry the head moved from: %+v", b)
		}
	}
	_ = root
}

func TestACompactionIsARowThatSaysWhatItFolded(t *testing.T) {
	l := newLog(t)
	first := l.append(itemEntry(msg("a", "user", "one"), ""))
	l.append(itemEntry(msg("b", "assistant", "r1"), "r1"))
	kept := l.append(itemEntry(msg("c", "user", "two"), ""))
	_ = first
	c, err := l.s.Compact(kept, msg("", "assistant", "we spoke of one"))
	if err != nil {
		t.Fatal(err)
	}
	c.TokensBefore = 1234
	c.Pinned = append(c.Pinned, msg("p", "user", "pinned"))
	id := l.append(c)

	m := l.v.Model()
	var fold *Row
	for i := range m.Rows {
		if m.Rows[i].Fold != nil {
			fold = &m.Rows[i]
		}
	}
	if fold == nil {
		t.Fatalf("no compaction row in %d rows", len(m.Rows))
	}
	f := fold.Fold
	if fold.EntryID != id || f.FirstKept != kept || f.SummaryLen != len("we spoke of one") || f.Pinned != 1 || f.TokensBefore != 1234 {
		t.Errorf("row = %+v fold = %+v, want entry %s first kept %s", fold, f, id, kept)
	}
}

// The old line's bookkeeping ends on the entry the head moved back to: its
// response and its run end are childless once the new run hangs from the
// entry itself. A tip resting on an entry another line continues past is
// not a branch.
func TestBookkeepingOfTheOldLineIsNotABranchOnceTheHeadIsExtended(t *testing.T) {
	l := newLog(t)
	l.append(agentsession.NewRunStart("run1", agentsession.SourceInput, ""))
	l.append(itemEntry(msg("a", "user", "one"), ""))
	r1 := l.append(itemEntry(msg("b", "assistant", "r1"), "resp1"))
	l.append(agentsession.NewRunEnd("run1", "done", "", nil))
	// The old line goes on: two, r2.
	l.append(agentsession.NewRunStart("run2", agentsession.SourceInput, ""))
	l.append(itemEntry(msg("c", "user", "two"), ""))
	r2 := l.append(itemEntry(msg("d", "assistant", "r2"), "resp2"))
	l.append(agentsession.NewRunEnd("run2", "done", "", nil))

	if err := l.s.Branch(r1); err != nil {
		t.Fatal(err)
	}
	mark, _ := l.s.MarkLeaf()
	l.append(mark)
	l.append(agentsession.NewRunStart("run3", agentsession.SourceInput, ""))
	l.append(itemEntry(msg("e", "user", "three"), ""))
	r3 := l.append(itemEntry(msg("f", "assistant", "r3"), "resp3"))
	l.append(agentsession.NewRunEnd("run3", "done", "", nil))

	m := l.v.Model()
	var got []string
	for _, b := range m.Branches {
		got = append(got, b.Leaf)
	}
	if len(got) != 2 || !(got[0] == r3 && got[1] == r2) {
		t.Errorf("branches rest on %v, want the new line %s then the old %s (and not %s)", got, r3, r2, r1)
	}
}

func TestCalleeLinksCarryTheirMembers(t *testing.T) {
	remote := agentsession.Callee{URL: "https://agents.example/a2a", Task: "task-7", Context: "ctx-3"}
	withSession := remote
	withSession.Session = "remote-session-1"
	cases := []struct {
		name  string
		entry *agentsession.LinkEntry
		want  Link
	}{
		{"in-process subsession", agentsession.NewSubsessionLink("child1", "call_1"),
			Link{Rel: agentsession.RelSubsession, Session: "child1", CallID: "call_1"}},
		{"peer", agentsession.NewPeerLink("call_2", remote),
			Link{Rel: agentsession.RelPeer, CallID: "call_2", URL: remote.URL, Task: "task-7", Context: "ctx-3"}},
		{"peer with a session", agentsession.NewPeerLink("call_2", withSession),
			Link{Rel: agentsession.RelPeer, Session: "remote-session-1", CallID: "call_2", URL: remote.URL, Task: "task-7", Context: "ctx-3"}},
		{"callee subsession without a session", agentsession.NewCalleeSubsessionLink("call_3", remote),
			Link{Rel: agentsession.RelSubsession, CallID: "call_3", URL: remote.URL, Task: "task-7", Context: "ctx-3"}},
		{"callee subsession with a session", agentsession.NewCalleeSubsessionLink("call_3", withSession),
			Link{Rel: agentsession.RelSubsession, Session: "remote-session-1", CallID: "call_3", URL: remote.URL, Task: "task-7", Context: "ctx-3"}},
		{"an unknown relation", agentsession.NewLinkEntry("x-reviewed_by", "reviewer1"),
			Link{Rel: "x-reviewed_by", Session: "reviewer1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := newLog(t)
			l.append(itemEntry(msg("a", "user", "q"), ""))
			c.want.Entry = l.append(c.entry)
			for _, kind := range []agentsession.ChangeKind{agentsession.Appended, agentsession.Reset} {
				if kind == agentsession.Reset {
					l.v.Record(agentsession.Change{Kind: agentsession.Reset, Session: l.s})
				}
				m := l.v.Model()
				if len(m.Links) != 1 || m.Links[0] != c.want {
					t.Errorf("after %v links = %+v, want [%+v]", kind, m.Links, c.want)
				}
			}
		})
	}
}

func TestACancelNoticeMarksTheLinkItNames(t *testing.T) {
	remote := agentsession.Callee{URL: "https://agents.example/a2a", Task: "task-7"}
	for _, rel := range []string{agentsession.RelPeer, agentsession.RelSubsession} {
		t.Run(rel, func(t *testing.T) {
			build := func() (*agentsession.Session, *agentsession.LinkEntry, *agentsession.LinkEntry, *agentsession.LinkEntry) {
				s := agentsession.New(agentsession.Header{})
				mustAppend(t, s, itemEntry(msg("a", "user", "q"), ""))
				link := agentsession.NewCalleeSubsessionLink("call_1", remote)
				if rel == agentsession.RelPeer {
					link = agentsession.NewPeerLink("call_1", remote)
				}
				mustAppend(t, s, link)
				other := agentsession.NewPeerLink("call_2", agentsession.Callee{URL: "https://other.example/a2a", Task: "task-8"})
				mustAppend(t, s, other)
				notice := agentsession.NewCancelSentLink(link)
				mustAppend(t, s, notice)
				return s, link, other, notice
			}
			check := func(t *testing.T, what string, m Model, link, other *agentsession.LinkEntry) {
				t.Helper()
				if len(m.Links) != 2 {
					t.Fatalf("%s: links = %+v, want the two callees and no notice", what, m.Links)
				}
				for _, got := range m.Links {
					switch got.Entry {
					case link.ID:
						if !got.CancelSent || got.Rel != rel || got.URL != remote.URL || got.Task != "task-7" {
							t.Errorf("%s: the cancelled link = %+v", what, got)
						}
					case other.ID:
						if got.CancelSent {
							t.Errorf("%s: a link no notice names is marked: %+v", what, got)
						}
					default:
						t.Errorf("%s: a link the session does not hold as a callee: %+v", what, got)
					}
				}
			}

			// The link, then the notice, as the follower delivers them.
			s, link, other, notice := build()
			v := New()
			v.Record(agentsession.Change{Kind: agentsession.Snapshot, Session: agentsession.New(agentsession.Header{})})
			for _, e := range s.Entries() {
				v.Record(agentsession.Change{Kind: agentsession.Appended, Session: s, ID: e.Base().ID, Entry: e})
			}
			check(t, "link then notice", v.Model(), link, other)
			// The notice delivered again is still not a link.
			v.Record(agentsession.Change{Kind: agentsession.Appended, Session: s, ID: notice.ID, Entry: notice})
			check(t, "the notice again", v.Model(), link, other)
			// A reset reads them in again, once.
			v.Record(agentsession.Change{Kind: agentsession.Reset, Session: s})
			check(t, "after a reset", v.Model(), link, other)
			check(t, "a snapshot", At(s, ""), link, other)

			// The notice before its link marks nothing, as Callees reads
			// a path: a notice names a link written before it.
			v = New()
			v.Record(agentsession.Change{Kind: agentsession.Snapshot, Session: agentsession.New(agentsession.Header{})})
			for _, e := range []agentsession.Entry{notice, link, other} {
				v.Record(agentsession.Change{Kind: agentsession.Appended, Session: s, ID: e.Base().ID, Entry: e})
				if e == notice && len(v.Model().Links) != 0 {
					t.Errorf("the notice is listed as a link: %+v", v.Model().Links)
				}
			}
			if m := v.Model(); len(m.Links) != 2 || m.Links[0].CancelSent || m.Links[1].CancelSent {
				t.Errorf("notice then link: links = %+v, want the two callees unmarked", m.Links)
			}
		})
	}
}

// TestACancelNoticeBeforeItsLinkMarksNothing: a file (one a writer
// re-pointed, or another product's) whose notice comes before the link
// it names, read whole. The view marks nothing, as Callees passes it
// over.
func TestACancelNoticeBeforeItsLinkMarksNothing(t *testing.T) {
	newLink := func(parent string) *agentsession.LinkEntry {
		l := agentsession.NewPeerLink("call_1", agentsession.Callee{URL: "https://peer.example/a2a", Task: "t"})
		l.Parent, l.Timestamp = parent, time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
		return l
	}
	// The link's ID, from a session that holds it alone: IDs are
	// hashes, so the same link under the same parent has the same one.
	s := agentsession.New(agentsession.Header{ID: "s1"})
	root := mustAppend(t, s, itemEntry(msg("a", "user", "q"), ""))
	alone := agentsession.New(agentsession.Header{ID: "s1"})
	first, _ := s.Entry(root)
	if _, err := alone.Commit(first); err != nil {
		t.Fatal(err)
	}
	want := mustAppend(t, alone, newLink(root))

	// Commit is a read path's append: it holds the entry to the format,
	// not to the writer's rule that a notice follows its link.
	notice := &agentsession.LinkEntry{Rel: agentsession.RelPeer, CallID: "call_1", Target: want, Cancel: agentsession.CancelSent}
	notice.Parent = root
	if _, err := s.Commit(notice); err != nil {
		t.Fatal(err)
	}
	link := newLink(root)
	if _, err := s.Commit(link); err != nil {
		t.Fatal(err)
	}
	if link.ID != want {
		t.Fatalf("the link's ID is %q, not the %q the notice names", link.ID, want)
	}
	// With hashed IDs a link cannot follow a notice naming it on one
	// path, so the two are siblings: the notice on one branch, the link
	// on another, the notice first in the file.
	callees, err := s.Callees(link.ID)
	if err != nil || len(callees) != 1 || callees[0].Cancelled {
		t.Fatalf("Callees = %+v, %v: want the link, not cancelled", callees, err)
	}
	for _, m := range []Model{At(s, ""), func() Model {
		v := New()
		v.Record(agentsession.Change{Kind: agentsession.Snapshot, Session: s})
		v.Record(agentsession.Change{Kind: agentsession.Reset, Session: s})
		return v.Model()
	}()} {
		if len(m.Links) != 1 || m.Links[0].Entry != link.ID || m.Links[0].CancelSent {
			t.Errorf("links = %+v, want the peer unmarked", m.Links)
		}
	}
}

func TestACancelNoticeForNoCalleeLinkIsNotALink(t *testing.T) {
	l := newLog(t)
	l.append(itemEntry(msg("a", "user", "q"), ""))
	fork := l.append(agentsession.NewLinkEntry(agentsession.RelForkOf, "fork1"))
	// Notices a writer may not append, as a foreign file may hold them:
	// one naming a link of another relation and one naming no link.
	for i, target := range []string{fork, "missing"} {
		n := &agentsession.LinkEntry{Rel: agentsession.RelPeer, CallID: "call_1", Target: target, Cancel: agentsession.CancelSent}
		n.ID = "notice" + string(rune('0'+i))
		l.v.Record(agentsession.Change{Kind: agentsession.Appended, Session: l.s, ID: n.ID, Entry: n})
	}
	m := l.v.Model()
	if len(m.Links) != 1 || m.Links[0].Entry != fork || m.Links[0].CancelSent {
		t.Errorf("links = %+v, want the fork alone, unmarked", m.Links)
	}
}

func mustAppend(t *testing.T, s *agentsession.Session, e agentsession.Entry) string {
	t.Helper()
	id, err := s.Append(e)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
