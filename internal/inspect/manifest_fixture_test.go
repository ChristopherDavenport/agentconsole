package inspect_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChristopherDavenport/agentmemory"
	"github.com/ChristopherDavenport/agentturn"
)

// manifestFixture is one of agentmemory's testdata/manifest fixtures
// (v0.0.12), copied under testdata/agentmemory-manifest: the records a
// session holds in order, and after each the manifest in force by its
// hash and whether the fold refused the record.
type manifestFixture struct {
	Steps []struct {
		Note    string          `json:"note"`
		Record  json.RawMessage `json:"record"`
		Refused string          `json:"refused"`
		InForce string          `json:"in_force"`
	} `json:"steps"`
}

// The session pane folds the memory manifests on the line with
// agentmemory's own fold (#4): the manifest in force, a block the product
// left out (dropped), and the records refused, unresolved or malformed,
// are what agentmemory's fixtures say.
func TestTheManifestFoldIsAgentmemorys(t *testing.T) {
	files, err := filepath.Glob("testdata/agentmemory-manifest/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var fx manifestFixture
			if err := json.Unmarshal(data, &fx); err != nil {
				t.Fatal(err)
			}
			r := newRig(t, agentturn.Config{ModelName: "m", Model: &script{responses: []step{say("hi")}}})
			r.prompt("hello")
			refused := 0
			for _, st := range fx.Steps {
				r.annotate(agentmemory.ManifestNS, st.Record)
				m := r.model()
				s, err := r.in.Session(r.ctx, "", m.Tail, m.Entries)
				if err != nil {
					t.Fatal(err)
				}
				if st.Refused != "" {
					refused++
				}
				if len(s.ManifestRefused) != refused {
					t.Fatalf("%s: %d refused, want %d: %+v", st.Note, len(s.ManifestRefused), refused, s.ManifestRefused)
				}
				if st.Refused != "" {
					last := s.ManifestRefused[refused-1]
					if last.Unresolved != (st.Refused == "unresolved") {
						t.Errorf("%s: refused unresolved=%v, want %s", st.Note, last.Unresolved, st.Refused)
					}
				}
				if s.Manifest == nil {
					t.Fatalf("%s: no manifest in force", st.Note)
				}
				if got := s.Manifest.Hash(); got != st.InForce {
					t.Errorf("%s: in force %s, want %s", st.Note, got, st.InForce)
				}
				lines := joined(s.Lines())
				if s.Manifest.Dropped() != strings.Contains(lines, "memory block left out this turn") {
					t.Errorf("%s: dropped=%v but the pane says:\n%s", st.Note, s.Manifest.Dropped(), lines)
				}
			}
		})
	}
}
