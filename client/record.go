package client

import (
	"context"
	"fmt"
	"iter"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn/session"
)

// RecordOf is the session rec writes, followed on rec's store: the
// record of an agent in this process. The store must be a
// [agentsession.Follower] to follow and a [agentsession.Reader] to read
// a session whole beside the recorder writing it; it may be a
// [agentsession.RefStore] for Refs. The record is verified: it is the
// agent's own, written by its recorder with request hashes a reader can
// check.
//
// The followed session is rec's at the call: a head move rebases the
// recorder within its session, which a follower sees as a head change.
func RecordOf(rec *session.Recorder) (Record, error) {
	f, ok := rec.Store().(agentsession.Follower)
	if !ok {
		return nil, fmt.Errorf("client: store %T cannot be followed", rec.Store())
	}
	return record{rec: rec, store: f}, nil
}

type record struct {
	rec   *session.Recorder
	store agentsession.Follower
}

func (r record) Follow(ctx context.Context, from agentsession.Cursor) iter.Seq2[agentsession.Change, error] {
	return r.store.Follow(ctx, r.rec.SessionID(), from)
}

func (record) Verified() bool { return true }

// Read reads the session through the store's Reader, which takes no hold
// and writes nothing, so it works beside the recorder writing it.
func (r record) Read(ctx context.Context, id string) (*agentsession.Session, error) {
	if id == "" {
		id = r.rec.SessionID()
	}
	rd, ok := r.rec.Store().(agentsession.Reader)
	if !ok {
		return nil, fmt.Errorf("client: store %T cannot read a session without holding it", r.rec.Store())
	}
	return rd.Read(ctx, id)
}

// Refs lists the refs whose target is the session.
func (r record) Refs(ctx context.Context, id string) ([]agentsession.Ref, error) {
	if id == "" {
		id = r.rec.SessionID()
	}
	rs, ok := r.rec.Store().(agentsession.RefStore)
	if !ok {
		return nil, agentsession.ErrNoRefs
	}
	var out []agentsession.Ref
	for ref, err := range rs.ListRefs(ctx, "") {
		if err != nil {
			return nil, err
		}
		if ref.Target.Session == id {
			out = append(out, ref)
		}
	}
	return out, nil
}
