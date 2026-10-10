// Package client is what a terminal client reads an agent through and
// what it says to it. The view takes two things in: an event [Source],
// the agent's live stream (what is not committed yet), and a [Record],
// its session (what is). It sends one thing out: [Intent]s, plain values
// a [Controller] acts on. The view holds nothing it can act on; whoever
// gives it a Controller decides what an intent does, and what follows
// reaches the view as events and record entries, never as a return value.
//
// The rule behind the split is that the record is the truth for everything
// committed and live events carry only what is not. Package
// [github.com/ChristopherDavenport/agentconsole/view] turns the two
// streams into what a client renders, and package
// [github.com/ChristopherDavenport/agentconsole/console] runs the
// terminal client over them.
//
// agentturn's types are the vocabulary: its events, its answers. An
// [agentturn.Control] is a Source, and [Direct] maps intents onto one,
// the only call of a Control in agentconsole's library packages.
package client

import (
	"context"
	"iter"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"
)

// Cost prices one model call in US dollars. It reports false for a model
// it has no price for.
type Cost func(model string, usage openresponses.Usage) (float64, bool)

// Source is where the view's live events come from: agentturn's events
// and the agent's state. An [agentturn.Control] is one (in process,
// agentkit's Kit.Control; over a wire, agentturn's front/control Dial);
// the view needs nothing else of it.
type Source interface {
	// Subscribe calls fn with each event, in order, until unsubscribe,
	// as [agentturn.Control.Subscribe] does: a question still waiting is
	// sent a new subscriber first, and no other past event.
	Subscribe(fn func(context.Context, agentturn.Event) error) (unsubscribe func())
	// State is the agent's state now: whether a run goes, the calls
	// that wait.
	State() agentturn.State
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
