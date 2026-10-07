package view

import (
	"strings"
	"time"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/openresponses"
)

// Branch is one branch tip of the session, for a tree view.
type Branch struct {
	// Leaf is the item the branch rests on (Model.Leaves holds the same
	// IDs).
	Leaf string
	// Label names the branch by its newest message: who wrote it ("agent"
	// for the assistant role) and its first words. A branch with no
	// message on it is labelled "(no message)".
	Label string
	// Role is the role of that message.
	Role string
	// RunID is the run of the nearest run entry at or behind the tip.
	RunID string
	// Time is when the tip entry was written.
	Time time.Time
	// Current is set for the branch the view is on.
	Current bool
}

// Link is a link entry of the session: a subsession it spawned, a
// successor it was continued in, a fork or a judge.
type Link struct {
	// Rel is the link's relation: agentsession.RelSubsession,
	// RelContinuedIn, RelForkOf or RelJudgedBy.
	Rel string
	// Session is the other session.
	Session string
	// CallID is the call that spawned a subsession, when the link says.
	CallID string
	// Entry is the link entry's own ID.
	Entry string
}

// Origin is where a session came from, from its header: the session it
// was forked, continued or spawned from, and the entry it was forked at.
type Origin struct {
	// ParentSession is the header's parent_session: the origin of a
	// fork, the predecessor of a continuation, or the parent of a
	// subsession.
	ParentSession string
	// Base is the entry of ParentSession the session was forked at, "" for
	// a session that is not a fork.
	Base string
	// SpawnedBy is the header's spawned_by: the call that spawned the
	// session, for a subsession.
	SpawnedBy string
}

// labelHops bounds the walk behind a tip that looks for a message and a
// run: a branch of nothing but tool calls is labelled by what it has.
const labelHops = 64

// labelWords bounds a branch label.
const (
	labelWords = 8
	labelRunes = 60
)

// branchesOf describes the tips, newest first (the order of the file,
// reversed). The session is read and not kept.
func branchesOf(s *agentsession.Session, tips []string, current string) []Branch {
	out := make([]Branch, 0, len(tips))
	for i := len(tips) - 1; i >= 0; i-- {
		out = append(out, describe(s, tips[i], tips[i] == current))
	}
	return out
}

func describe(s *agentsession.Session, tip string, current bool) Branch {
	b := Branch{Leaf: tip, Current: current, Label: "(no message)"}
	if e, ok := s.Entry(tip); ok {
		b.Time = e.Base().Timestamp
	}
	haveMsg, haveRun := false, false
	for id, hops := tip, 0; id != "" && hops < labelHops && !(haveMsg && haveRun); hops++ {
		e, ok := s.Entry(id)
		if !ok {
			break
		}
		switch x := e.(type) {
		case *agentsession.ItemEntry:
			if m, ok := x.Item.(*openresponses.Message); ok && !haveMsg && x.IsVisible() {
				haveMsg = true
				b.Role = string(m.Role)
				who := string(m.Role)
				if m.Role == openresponses.RoleAssistant {
					who = "agent"
				}
				b.Label = who + ": " + firstWords(m.Text())
			}
		case *agentsession.RunEntry:
			if !haveRun {
				haveRun, b.RunID = true, x.RunID
			}
		}
		id = e.Base().Parent
	}
	return b
}

// firstWords is the start of a text, on one line.
func firstWords(text string) string {
	words := strings.Fields(text)
	more := false
	if len(words) > labelWords {
		words, more = words[:labelWords], true
	}
	out := strings.Join(words, " ")
	if r := []rune(out); len(r) > labelRunes {
		out, more = string(r[:labelRunes]), true
	}
	if more {
		out += "…"
	}
	if out == "" {
		return "(empty)"
	}
	return out
}

// At renders the line of s that ends at leaf, as a view attached to the
// session with its head there would, and nothing live: no overlay, and
// no turn. It reads s and keeps nothing, so a caller can pass a session
// it read for the purpose, the session a fork came from or one a link
// names, and show it without following it. Its Permissions and CutOff
// are the record's reading of the line, which a read-only display does
// not offer to answer.
func At(s *agentsession.Session, leaf string) Model {
	v := New()
	v.Record(agentsession.Change{Kind: agentsession.Snapshot, Session: s})
	if leaf != "" {
		v.Record(agentsession.Change{Kind: agentsession.Head, Session: s, Leaf: leaf})
	}
	return v.Model()
}
