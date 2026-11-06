package inspect_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChristopherDavenport/agentpolicy"
	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"

	"github.com/ChristopherDavenport/agentconsole/internal/inspect"
)

// shortEntry is an entry ID as the panes shorten it.
func shortEntry(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

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

// agentpolicy v0.0.20 records a replacement of the policy as a verdict
// of kind replace. It ends the rules Engine.Grant and GrantOver added,
// which joined the policy replaced; the grant sets stay (agentpolicy
// RFC 0001, Grants, Replace). The session pane shows the plain grant
// ended at the replacement, the set's in force, and the replacement
// with its reason and its new default (#51).
func TestAReplacementEndsThePlainGrants(t *testing.T) {
	cfg := agentturn.Config{ModelName: "m", Tools: []agenttool.Tool{upper()},
		Model: &script{responses: []step{say("one"), callTool("call_1", "upper", `{"text":"a"}`), say("two")}}}
	r := newRig(t, cfg)
	eng, err := agentpolicy.Build(agentpolicy.Policy{Default: agentpolicy.Ask()}, nil,
		agentpolicy.WithObserver(func(ctx context.Context, v agentpolicy.Verdict) {
			ns, data := v.Record()
			if _, err := r.rec.Annotate(r.ctx, ns, json.RawMessage(data)); err != nil {
				t.Errorf("record %+v: %v", v, err)
			}
		}))
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Grant(r.ctx, agentpolicy.Rule{Tool: "upper", Source: agentpolicy.Source{Name: "agentskill:one", Trusted: true}}); err != nil {
		t.Fatal(err)
	}
	if _, refused := eng.GrantSet(r.ctx, agentpolicy.RuleSet{Source: agentpolicy.Source{Name: "agentskill:two", Trusted: true},
		Allow: []agentpolicy.Rule{{Tool: "upper", Source: agentpolicy.Source{Name: "agentskill:two", Trusted: true}}}}); len(refused) != 0 {
		t.Fatalf("refused %+v", refused)
	}
	r.prompt("go")
	if err := eng.SetPolicy(r.ctx, agentpolicy.Policy{Default: agentpolicy.Deny().WithNote("plan mode")}, "the user switched to plan mode"); err != nil {
		t.Fatal(err)
	}
	r.prompt("call it")

	m := r.model()
	s, err := r.in.Session(r.ctx, "", m.Tail, m.Entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Replacements) != 1 {
		t.Fatalf("replacements = %+v, want one", s.Replacements)
	}
	rp := s.Replacements[0]
	if rp.Default != "deny" || rp.DefaultNote != "plan mode" || !strings.Contains(rp.Reason, "the user switched to plan mode") {
		t.Errorf("replacement = %+v", rp)
	}
	byRule := map[string]inspect.Grant{}
	for _, g := range s.Grants {
		byRule[g.Source] = g
	}
	if g := byRule["agentskill:one"]; g.Ended != rp.Entry || !g.PolicyReplaced {
		t.Errorf("the plain grant: %+v, want ended by the replacement %s", g, rp.Entry)
	}
	if g := byRule["agentskill:two"]; g.Ended != "" {
		t.Errorf("the grant set ended: %+v", g)
	}
	// A call made after it is made under it.
	call := r.entry(rowWith(t, m, isCall), m)
	if c := call.Call; c == nil || c.Policy == nil || c.Policy.Entry != rp.Entry {
		t.Errorf("the call's policy in force: %+v", call.Call)
	}
	if want := "policy:   in force: replaced at " + shortEntry(rp.Entry); !strings.Contains(joined(call.Lines()), want) {
		t.Errorf("no %q in the call's pane:\n%s", want, joined(call.Lines()))
	}
	text := joined(s.Lines())
	for _, want := range []string{
		"one allowed upper, ended by the policy's replacement at " + shortEntry(rp.Entry),
		"two allowed upper, in force",
		"policy:   replaced at " + shortEntry(rp.Entry) + ", default deny (plan mode): replaced the policy: the user switched to plan mode",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
}
