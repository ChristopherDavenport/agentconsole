package inspect_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ChristopherDavenport/agentsession"

	"github.com/ChristopherDavenport/agentconsole/internal/inspect"
)

// fileSource serves one session read from a file, and no refs.
type fileSource struct{ s *agentsession.Session }

func (f fileSource) Read(context.Context, string) (*agentsession.Session, error) { return f.s, nil }
func (f fileSource) Refs(context.Context, string) ([]agentsession.Ref, error) {
	return nil, agentsession.ErrNoRefs
}

func readFixture(t *testing.T, data []byte) *agentsession.Session {
	t.Helper()
	s, err := agentsession.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A redacted session says so: the session pane names its original by the
// pseudonym redacted_from carries (never as a session to open) and the
// profile, and counts its redacted and native entries; an entry's pane
// gives the original entries it was made from (#38). A redacted session
// whose ID is not the one its redacted_from derives is noted, as
// agentsession verify notes it, without failing the session.
func TestARedactedSessionShowsItsRedaction(t *testing.T) {
	data, err := os.ReadFile("testdata/agentsession/redacted-ids.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	s := readFixture(t, data)
	in := inspect.New(fileSource{s})
	ctx := context.Background()
	sp, err := in.Session(ctx, s.ID(), s.Leaf(), s.Len())
	if err != nil {
		t.Fatal(err)
	}
	r := sp.Redaction
	if r == nil || r.From != "238049a1-ae4a-823e-8000-b19bb9a2be91" || r.Profile != "example.redact@1" || r.Redacted != 8 || r.Native != 0 || r.IDErr != "" {
		t.Fatalf("redaction = %+v", r)
	}
	text := joined(sp.Lines())
	if !strings.Contains(text, "redacted from 238049a1-ae4a-823e-8000-b19bb9a2be91 (profile example.redact@1)") || !strings.Contains(text, "8 redacted, 0 native") {
		t.Errorf("the session pane:\n%s", text)
	}
	if strings.Contains(text, "FAILED") || strings.Contains(text, "mismatch:") {
		t.Errorf("a redacted session verifies:\n%s", text)
	}

	var item string
	for _, e := range s.Path(s.Leaf()) {
		if _, ok := e.(*agentsession.ItemEntry); ok {
			item = e.Base().ID
			break
		}
	}
	e, err := in.Entry(ctx, s.ID(), s.Leaf(), item, s.Len())
	if err != nil {
		t.Fatal(err)
	}
	if !e.Redacted || len(e.Spans) != 1 || e.Spans[0].From != 2 {
		t.Errorf("entry %+v", e)
	}
	if text := joined(e.Lines()); !strings.Contains(text, "made from original entry 2") {
		t.Errorf("the entry pane:\n%s", text)
	}

	// The same file under another ID: noted, not failed.
	other := bytes.Replace(data, []byte(`"id":"f9cf49c3-f8ed-5a2e-9ff4-fffe8e9f7a1a"`), []byte(`"id":"00000000-0000-4000-8000-000000000000"`), 1)
	s = readFixture(t, other)
	sp, err = inspect.New(fileSource{s}).Session(ctx, s.ID(), s.Leaf(), s.Len())
	if err != nil {
		t.Fatal(err)
	}
	if sp.Redaction == nil || sp.Redaction.IDErr == "" {
		t.Fatalf("redaction = %+v, want the ID noted", sp.Redaction)
	}
	text = joined(sp.Lines())
	if !strings.Contains(text, "not the one its redacted_from derives") || strings.Contains(text, "FAILED") {
		t.Errorf("the session pane:\n%s", text)
	}
}
