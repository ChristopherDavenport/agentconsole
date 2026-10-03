package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentsession/cas"
	"github.com/ChristopherDavenport/agentsession/jsonl"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"
)

// openStore opens the session store at root: kind is "jsonl" or "cas".
func openStore(kind, root string) (agentsession.Store, func() error, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, nil, err
	}
	switch kind {
	case "jsonl":
		s, err := jsonl.Open(root)
		if err != nil {
			return nil, nil, err
		}
		return s, s.Close, nil
	case "cas":
		s, err := cas.Open(root)
		if err != nil {
			return nil, nil, err
		}
		return s, s.Close, nil
	}
	return nil, nil, fmt.Errorf("unknown store kind %q, want jsonl or cas", kind)
}

// defaultStoreRoot is where sessions live unless told otherwise.
func defaultStoreRoot() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "agentconsole", "sessions")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".local", "share", "agentconsole", "sessions")
	}
	return filepath.Join(".agentconsole", "sessions")
}

// harnessName and harnessVersion name this client in the header of a
// session it starts.
const (
	harnessName    = "agentconsole"
	harnessVersion = "dev"
)

// newHeader is the header of a session the client starts: the writer and
// where it runs. session.WithHarness alone names the writer only in the
// headers of child sessions, so the root's has to say it here.
func newHeader() agentsession.Header {
	h := agentsession.Header{Harness: &agentsession.Harness{Name: harnessName, Version: harnessVersion}}
	if wd, err := os.Getwd(); err == nil {
		h.CWD = wd
	}
	return h
}

// refPrefix marks a session given by the name of a ref.
const refPrefix = "ref:"

// openSession starts or resumes the session the flags name and returns
// its recorder and the agent options that seed an agent with what the
// session holds.
//
//   - resume "ID" resumes that session; resume "ref:NAME" resumes the
//     session the ref points to, and fails when there is none.
//   - conversation NAME resumes the session the ref NAME points to,
//     creating one and the ref when there is none (agentsession's
//     SessionFor), so a named conversation continues from run to run.
//   - neither starts a new session.
func openSession(ctx context.Context, st agentsession.Store, resume, conversation string, opts ...session.Option) (*session.Recorder, []agentturn.Option, error) {
	if resume != "" && conversation != "" {
		return nil, nil, errors.New("--session and --conversation are exclusive")
	}
	var id string
	switch {
	case strings.HasPrefix(resume, refPrefix):
		rs, ok := st.(agentsession.RefStore)
		if !ok {
			return nil, nil, agentsession.ErrNoRefs
		}
		t, err := rs.ResolveRef(ctx, strings.TrimPrefix(resume, refPrefix))
		if err != nil {
			return nil, nil, fmt.Errorf("resolve %s: %w", resume, err)
		}
		id = t.Session
	case resume != "":
		id = resume
	case conversation != "":
		s, err := agentsession.SessionFor(ctx, st, conversation, newHeader())
		if err != nil {
			return nil, nil, fmt.Errorf("conversation %s: %w", conversation, err)
		}
		id = s.ID()
	default:
		rec, _, err := session.Start(ctx, st, newHeader(), opts...)
		return rec, nil, err
	}
	rec, _, err := session.Resume(ctx, st, id, opts...)
	if err != nil {
		return nil, nil, err
	}
	// Resume may have closed a run that was cut off: read the session as
	// it stands now. The store hands back the session it holds open.
	s, err := st.Open(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	ao, err := session.AgentOptions(s, rec.ReadOptions()...)
	if err != nil {
		return nil, nil, fmt.Errorf("seed the agent from %s: %w", id, err)
	}
	return rec, ao, nil
}
