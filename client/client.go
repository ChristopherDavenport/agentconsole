// Package client is what a terminal client reads an agent through. The
// agent is driven through agentturn's control contract,
// [agentturn.Control], whose Subscribe is the live stream: what is not
// committed yet. The session is read through a [Record]: what is.
//
// The rule behind the split is that the record is the truth for everything
// committed and live events carry only what is not. Package
// [github.com/ChristopherDavenport/agentconsole/view] turns the two
// streams into what a client renders, and package
// [github.com/ChristopherDavenport/agentconsole/console] runs the
// terminal client over a control and a record.
//
// Nothing here runs an agent. The control is the loop's or the kit's
// (agentkit's Kit.Control in process, agentturn's front/control Dial over
// a wire), and the record is a store's.
package client

import (
	"context"
	"iter"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/openresponses"
)

// Cost prices one model call in US dollars. It reports false for a model
// it has no price for.
type Cost func(model string, usage openresponses.Usage) (float64, bool)

// HeadMover is a control that moves the agent's head, which
// [agentturn.Control] does not: agentkit's Control does. A client
// asserts it on the control it was given, and without it shows the
// session's tree read only.
type HeadMover interface {
	// ContinueFrom moves the agent's head to the entry: the next prompt
	// continues the conversation from there, on a branch of the
	// session. It is how a client acts on what it saw in the tree, and
	// the one control that moves the record's head. It is refused while
	// a run goes. The move is on the record, so a follower sees the head
	// move and a reopened session resumes there.
	ContinueFrom(ctx context.Context, entryID string) error
}

// Record is the agent's session as a client follows it.
type Record interface {
	// Follow yields a Snapshot first, or with a cursor the changes after
	// it, and then each change as the store accepts it, as
	// agentsession's Follower does.
	Follow(ctx context.Context, from agentsession.Cursor) iter.Seq2[agentsession.Change, error]
	// Verified reports whether the record is the agent's own, hashed and
	// checkable, or one synthesized from what a foreign agent said.
	Verified() bool
	// Read reads a session whole, as the caller's own: the followed one
	// for the empty ID, or another the store holds (the origin of a
	// fork, a subsession, a successor). It is what the tree and the
	// detail panes are computed from, on demand and off the follow: the
	// session it returns is a snapshot no later append reaches.
	Read(ctx context.Context, sessionID string) (*agentsession.Session, error)
	// Refs lists the refs that point at the session (the empty ID is the
	// followed one), by name. A store that keeps no refs answers with
	// agentsession.ErrNoRefs.
	Refs(ctx context.Context, sessionID string) ([]agentsession.Ref, error)
}
