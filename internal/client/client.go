// Package client is the contract between a terminal client and the agent
// it drives. A backend gives the client three things over one vocabulary,
// the stack's own types: Control to act on the agent, Live for what is not
// committed yet, and Record for what is.
//
// The rule behind the split is that the record is the truth for everything
// committed and live events carry only what is not. The package
// internal/view turns the two streams into what a client renders.
package client

import (
	"context"
	"iter"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"
)

// Backend is one agent the client drives.
type Backend interface {
	Control() Control
	// Live streams the uncommitted part of the agent's activity: deltas,
	// open tool calls, turn state, permission requests. It runs until ctx
	// is done. It subscribes when it is called, so a client that calls it
	// and then starts a run misses nothing; events from before the call
	// are not replayed, and a run already going is read from
	// Control.State.
	Live(ctx context.Context) iter.Seq2[LiveEvent, error]
	// Record follows the agent's session.
	Record() Record
}

// Control acts on the agent.
//
// Prompt and Answer block until the run they start ends, as the agent's
// own do, and return the error that kept it from starting or that ended
// it. A client calls them from a goroutine of its own and learns how the
// run went from Live's [RunEnded]. Steer and Abort return at once.
type Control interface {
	Prompt(ctx context.Context, items ...openresponses.Item) error
	Steer(items ...openresponses.Item)
	Abort()
	// Answer answers the calls a run left pending and continues it: the
	// reply to a permission request.
	Answer(ctx context.Context, answers ...agentturn.Answer) error
	State() agentturn.State
	// ContinueFrom moves the agent's head to the entry: the next prompt
	// continues the conversation from there, on a branch of the session.
	// It is how a client acts on what it saw in the tree, and the one
	// control that moves the record's head. It is refused while a run
	// goes. The move is recorded as a leaf label, so a follower sees the
	// head move and a reopened session resumes there.
	ContinueFrom(ctx context.Context, entryID string) error
}

// Record is the agent's session as a client follows it.
type Record interface {
	// Follow yields a Snapshot first, or with a cursor the changes after
	// it, and then each change as the store accepts it, as
	// agentsession's Follower does.
	Follow(ctx context.Context, from agentsession.Cursor) iter.Seq2[agentsession.Change, error]
	// Verified reports whether the record is the agent's own, hashed and
	// checkable, or one the backend synthesized from what it was told.
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
