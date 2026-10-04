// Package inspect computes the record-detail panes: what the session says
// about one entry (its response and whether the request checks out, a
// call's decisions and dispatch, a compaction's fold) and about the
// session as a whole (its header, its verification, its configuration, its
// refs, its memory manifest).
//
// It works on a snapshot of the session read from the backend, never on
// the follower's session, and it is meant to be called off the UI's
// goroutine and only for what the user selects. A response's verification
// is cached for good (a response entry and the path behind it never
// change), and a snapshot is reused while the session holds no more
// entries than it did.
//
// The records other products write beside the loop's are decoded here
// from their JSON, so the client depends on none of them: the policy
// verdicts of agentpolicy ("agentpolicy:verdict"), the skill grants
// agentkit makes of them, and agentmemory's manifest ("agentmemory:render").
// A record that does not decode is left out of the pane, not an error.
package inspect

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn/session"

	"github.com/ChristopherDavenport/agentconsole/client"
)

// Source is where a snapshot of a session and its refs come from; it is
// client.Record.
type Source interface {
	Read(ctx context.Context, sessionID string) (*agentsession.Session, error)
	Refs(ctx context.Context, sessionID string) ([]agentsession.Ref, error)
}

// Inspector computes detail for one source, and caches what is costly.
// It is safe for concurrent use.
type Inspector struct {
	src Source

	// cost prices one model call, when a host gave one. It is asked for
	// each call on a viewed line whose session pane is computed.
	cost client.Cost

	// readMu serializes reading, so two panes asked for at once read the
	// session once.
	readMu sync.Mutex
	mu     sync.Mutex
	snaps  map[string]*agentsession.Session
	verify map[string]Verify
	reads  int
}

// Option configures an [Inspector].
type Option func(*Inspector)

// WithCost sets the function that prices one model call, in US dollars.
// A call it does not price leaves the session pane's cost unpriced.
func WithCost(fn client.Cost) Option { return func(in *Inspector) { in.cost = fn } }

// New returns an inspector over src.
func New(src Source, opts ...Option) *Inspector {
	in := &Inspector{src: src, snaps: map[string]*agentsession.Session{}, verify: map[string]Verify{}}
	for _, o := range opts {
		o(in)
	}
	return in
}

// Reads is how many times the source was read, for a test of the cache.
func (in *Inspector) Reads() int {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.reads
}

// snapshot returns a read of the session holding at least entries
// entries: the last one read when it does, and a new read otherwise.
func (in *Inspector) snapshot(ctx context.Context, id string, entries int) (*agentsession.Session, error) {
	in.readMu.Lock()
	defer in.readMu.Unlock()
	in.mu.Lock()
	s := in.snaps[id]
	in.mu.Unlock()
	if s != nil && s.Len() >= entries {
		return s, nil
	}
	s, err := in.src.Read(ctx, id)
	if err != nil {
		return nil, err
	}
	in.mu.Lock()
	in.snaps[id] = s
	in.reads++
	in.mu.Unlock()
	return s, nil
}

// VerifyState says how far a response's request could be checked.
type VerifyState int

// Verification states.
const (
	// Verified: the request rebuilt from the path hashes to the recorded
	// request hash.
	Verified VerifyState = iota
	// Unhashed: the response recorded no hash, because its writer could
	// not stand behind one. Why says what the record gives as the cause.
	Unhashed
	// Mismatch: the rebuilt request hashes to something else.
	Mismatch
	// Failed: the request could not be rebuilt at all.
	Failed
)

// String names the state.
func (s VerifyState) String() string {
	switch s {
	case Verified:
		return "verified"
	case Unhashed:
		return "unhashed"
	case Mismatch:
		return "MISMATCH"
	}
	return "failed"
}

// Verify is the result of checking one response.
type Verify struct {
	State VerifyState
	// Why is the cause an unhashed response names, or the error of a
	// mismatch or a failure.
	Why string
}

// verifyResponse checks one response entry of the snapshot, once.
func (in *Inspector) verifyResponse(s *agentsession.Session, path []agentsession.Entry, at int) Verify {
	resp := path[at].(*agentsession.ResponseEntry)
	key := s.ID() + "|" + resp.ID
	in.mu.Lock()
	v, ok := in.verify[key]
	in.mu.Unlock()
	if ok {
		return v
	}
	err := s.Verify(resp.ID)
	switch {
	case err == nil:
		v = Verify{State: Verified}
	case errors.Is(err, agentsession.ErrNoHash):
		v = Verify{State: Unhashed, Why: unhashedCause(path, at)}
	case errors.Is(err, agentsession.ErrHashMismatch):
		v = Verify{State: Mismatch, Why: err.Error()}
	default:
		v = Verify{State: Failed, Why: err.Error()}
	}
	in.mu.Lock()
	in.verify[key] = v
	in.mu.Unlock()
	return v
}

// unhashedCause is what the record says of a response with no hash: the
// agentturn:unhashed entry nearest behind it. The recorder writes one
// before the first response left without a hash for a cause, and again
// when the cause changes or after a response that was hashed.
func unhashedCause(path []agentsession.Entry, at int) string {
	for i := at - 1; i >= 0; i-- {
		c, ok := path[i].(*agentsession.CustomEntry)
		if !ok || c.NS != session.UnhashedNS {
			continue
		}
		var u session.Unhashed
		if err := jsonUnmarshal(c.Data, &u); err != nil {
			return "an " + session.UnhashedNS + " entry that does not decode"
		}
		why := u.Reason
		if u.Sent != nil || u.Recorded != nil {
			why += fmt.Sprintf(" (input item %d: sent %s, the path has %s)", u.Index, describeItemRef(u.Sent), describeItemRef(u.Recorded))
		}
		return why
	}
	return "no " + session.UnhashedNS + " entry names a cause"
}

func describeItemRef(r *session.UnhashedItem) string {
	switch {
	case r == nil:
		return "nothing"
	case r.CallID != "":
		return r.Type + " " + r.CallID
	case r.ID != "":
		return r.Type + " " + r.ID
	}
	return r.Type
}
