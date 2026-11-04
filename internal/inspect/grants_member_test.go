package inspect_test

import (
	"encoding/json"
	"testing"

	"github.com/ChristopherDavenport/agentpolicy"
	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
)

// recordVerdict annotates the record agentpolicy itself writes for v.
func (r *rig) recordVerdict(v agentpolicy.Verdict) {
	r.t.Helper()
	ns, data := v.Record()
	if _, err := r.rec.Annotate(r.ctx, ns, json.RawMessage(data)); err != nil {
		r.t.Fatal(err)
	}
}

func activate(src, scope string) agentpolicy.Verdict {
	return agentpolicy.Verdict{Action: agentturn.Allow, Reason: "activated the rules granted by " + src, By: agentpolicy.ByPolicy,
		Grant: agentpolicy.KindActivate, Source: src, Scope: scope, Set: &agentpolicy.RuleSet{Source: agentpolicy.Source{Name: src, Trusted: true}}}
}

func grantSet(src, scope string) agentpolicy.Verdict {
	rule := agentpolicy.Rule{Tool: "upper", Source: agentpolicy.Source{Name: src, Trusted: true}}
	return agentpolicy.Verdict{Tool: "upper", Action: agentturn.Allow, Rule: &rule, Reason: "granted upper by " + src, By: agentpolicy.ByPolicy,
		Grant: agentpolicy.KindGrantSet, Source: src, Scope: scope}
}

// agentpolicy v0.0.19 records what a grant or a revocation did as
// members (grant, source, scope), and a reader takes them from there
// rather than from the reason: a revocation ends only the grants of its
// scope (and source), and an activation replaces its source's set under
// its scope.
func TestGrantsAreReadFromTheRecordsMembers(t *testing.T) {
	cfg := agentturn.Config{ModelName: "m", Tools: []agenttool.Tool{upper()},
		Model: &script{responses: []step{callTool("call_1", "upper", `{"text":"a"}`), say("one")}}}
	r := newRig(t, cfg)
	r.recordVerdict(activate("agentskill:one", "a"))
	r.recordVerdict(grantSet("agentskill:one", "a"))
	r.recordVerdict(activate("agentskill:one", "b"))
	r.recordVerdict(grantSet("agentskill:one", "b"))
	r.recordVerdict(activate("agentskill:two", "b"))
	r.recordVerdict(grantSet("agentskill:two", "b"))
	// The same set activated again under b: it replaces the first.
	r.recordVerdict(activate("agentskill:two", "b"))
	r.recordVerdict(grantSet("agentskill:two", "b"))
	r.prompt("go")
	// one's set under b goes; a's stays.
	r.recordVerdict(agentpolicy.Verdict{Action: agentturn.Block, Reason: "revoked the rules granted by agentskill:one", By: agentpolicy.ByPolicy,
		Grant: agentpolicy.KindRevoke, Source: "agentskill:one", Scope: "b"})
	// Scope a's sets go; b's stay.
	r.recordVerdict(agentpolicy.Verdict{Action: agentturn.Block, Reason: "revoked the rules granted under a", By: agentpolicy.ByPolicy,
		Grant: agentpolicy.KindRevokeScope, Scope: "a"})

	m := r.model()
	s, err := r.in.Session(r.ctx, "", m.Tail, m.Entries)
	if err != nil {
		t.Fatal(err)
	}
	type key struct{ source, scope string }
	ended := map[key][]bool{}
	for _, g := range s.Grants {
		ended[key{g.Source, g.Scope}] = append(ended[key{g.Source, g.Scope}], g.Ended != "")
	}
	want := map[key][]bool{
		{"agentskill:one", "a"}: {true},
		{"agentskill:one", "b"}: {true},
		// The first set was replaced by the second, which holds.
		{"agentskill:two", "b"}: {true, false},
	}
	for k, w := range want {
		got := ended[k]
		if len(got) != len(w) {
			t.Errorf("%v: %d grants, want %d (%+v)", k, len(got), len(w), s.Grants)
			continue
		}
		for i := range w {
			if got[i] != w[i] {
				t.Errorf("%v grant %d ended=%v, want %v", k, i, got[i], w[i])
			}
		}
	}
	if len(s.Grants) != 4 {
		t.Errorf("grants = %+v, want 4", s.Grants)
	}
}
