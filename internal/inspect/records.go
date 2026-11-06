package inspect

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/ChristopherDavenport/agentmemory"
	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn/session"
)

func jsonUnmarshal(data json.RawMessage, v any) error { return json.Unmarshal(data, v) }

// VerdictNS is where agentpolicy records a verdict, and the shape it
// writes (agentpolicy v0.0.11, record.go); the member names are the
// format's.
const VerdictNS = "agentpolicy:verdict"

// SkillSourcePrefix opens the source name of a rule granted by a skill
// (agentkit names its sources "agentskill:" and the skill's listed name).
const SkillSourcePrefix = "agentskill:"

// skillReadNS is where agentskill records a read of a skill, whose name
// member is the skill's listed name.
const skillReadNS = "agentskill:read"

// skillReads is the set of skills the path holds a read record of.
func skillReads(path []agentsession.Entry) map[string]bool {
	reads := map[string]bool{}
	for _, e := range path {
		c, ok := e.(*agentsession.CustomEntry)
		if !ok || c.NS != skillReadNS {
			continue
		}
		var r struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(c.Data, &r) == nil && r.Name != "" {
			reads[r.Name] = true
		}
	}
	return reads
}

// skillOf names the skill a rule source stands for: agentkit's default
// "agentskill:NAME", or a product's own naming ("skill:NAME" in dax) when
// the path holds a read of the skill NAME, which is how a source the
// product named is told from a settings file's.
func skillOf(source string, reads map[string]bool) (string, bool) {
	if name, ok := strings.CutPrefix(source, SkillSourcePrefix); ok {
		return name, true
	}
	if i := strings.LastIndex(source, ":"); i >= 0 && reads[source[i+1:]] {
		return source[i+1:], true
	}
	return "", false
}

// revokedPrefix opens the reason of the verdict that ends a source's
// grants (agentkit v0.0.7, grants.go).
const revokedPrefix = "revoked the rules granted by "

// Engine.RevokeScope journals "revoked the rules granted under <scope>",
// or the unscoped form (agentpolicy v0.0.11, grant.go). A grant's verdict
// carries no scope, so a reader cannot tell which grants the scope held:
// agentkit reads the revocation of its own scope as ending every grant of
// the conversation, and so does this, for either form. A session written
// by several scopes at once would show too many grants ended.
const (
	revokedScopePrefix = "revoked the rules granted under "
	revokedUnscoped    = "revoked the rules granted without a scope"
)

// Verdict is a policy verdict as the session holds it.
type Verdict struct {
	Entry      string `json:"-"`
	RunID      string `json:"run_id,omitempty"`
	Turn       int    `json:"turn,omitempty"`
	CallID     string `json:"call_id,omitempty"`
	Tool       string `json:"tool,omitempty"`
	Guard      string `json:"guard,omitempty"`
	Action     string `json:"action"`
	Rule       string `json:"rule,omitempty"`
	Source     string `json:"source,omitempty"`
	SourceHash string `json:"source_hash,omitempty"`
	Note       string `json:"note,omitempty"`
	Reason     string `json:"reason,omitempty"`
	By         string `json:"by,omitempty"`
	Held       bool   `json:"held,omitempty"`
	Subject    string `json:"subject,omitempty"`
	Confined   string `json:"confined,omitempty"`
	// Grant, with Source and Scope, says what a grant or a revocation
	// did (agentpolicy v0.0.19).
	Grant string `json:"grant,omitempty"`
	Scope string `json:"scope,omitempty"`
	// Policy is the policy a replacement put in force (agentpolicy
	// v0.0.20, grant "replace").
	Policy *policyRecord `json:"policy,omitempty"`
}

// policyRecord is what agentpolicy records of a policy a replacement put
// in force: its default and the default's note, and its sources by name.
// The rules it lists are not read.
type policyRecord struct {
	Default     string `json:"default"`
	DefaultNote string `json:"default_note,omitempty"`
	Sources     []struct {
		Name string `json:"name"`
	} `json:"sources,omitempty"`
}

// Replacement is a replacement of the policy (agentpolicy's
// Engine.SetPolicy): the verdict entry, the reason as recorded ("replaced
// the policy: <the host's reason>"), the new default and its note, and
// the sources the new policy was merged from. Decisions after it on the
// path are made under it.
type Replacement struct {
	Entry       string
	Reason      string
	Default     string
	DefaultNote string
	Sources     []string
}

// replacementOf reads a replacement verdict.
func replacementOf(v Verdict) Replacement {
	r := Replacement{Entry: v.Entry, Reason: v.Reason}
	if p := v.Policy; p != nil {
		r.Default, r.DefaultNote = p.Default, p.DefaultNote
		for _, src := range p.Sources {
			r.Sources = append(r.Sources, src.Name)
		}
	}
	return r
}

// replacements are the policy's replacements on path, in order.
func replacements(path []agentsession.Entry) []Replacement {
	var out []Replacement
	for _, e := range path {
		if v, ok := verdictOf(e); ok && v.Grant == kindReplace {
			out = append(out, replacementOf(v))
		}
	}
	return out
}

func verdictOf(e agentsession.Entry) (Verdict, bool) {
	c, ok := e.(*agentsession.CustomEntry)
	if !ok || c.NS != VerdictNS {
		return Verdict{}, false
	}
	var v Verdict
	if json.Unmarshal(c.Data, &v) != nil || v.Action == "" {
		return Verdict{}, false
	}
	v.Entry = c.ID
	return v, true
}

// Grant is a rule a skill's read granted, and what became of it.
type Grant struct {
	// Skill is the skill's listed name, Source the source the rule was
	// granted under.
	Skill  string
	Source string
	// Scope is the grant scope the rule was granted under, "" for none
	// or a record that does not say (agentpolicy before v0.0.19).
	Scope string
	Rule  string
	// Since is the verdict entry that recorded the grant.
	Since string
	// Ended is the verdict entry that revoked the source's grants, "" while
	// they hold on the whole path; Replaced says it was the activation of
	// the source's set again under the scope, not a revocation, and
	// PolicyReplaced that it was a replacement of the policy, which ends
	// a plain grant (Engine.Grant or GrantOver) with the policy it joined.
	Ended          string
	Replaced       bool
	PolicyReplaced bool
}

// The kinds agentpolicy (v0.0.19, RFC 0001 draft 0.4, The record)
// writes in a grant's or a revocation's grant member. A reader takes
// the kind, source and scope from the members and parses the reason
// only for a record written before them.
const (
	kindGrant       = "grant"
	kindGrantOver   = "grant_over"
	kindGrantSet    = "grant_set"
	kindActivate    = "activate"
	kindRevoke      = "revoke"
	kindRevokeScope = "revoke_scope"
	// kindReplace is a replacement of the policy (agentpolicy v0.0.20,
	// RFC 0001 draft 0.5, Grants): it ends the plain grants, which
	// joined the policy replaced, and leaves the grant sets.
	kindReplace = "replace"
)

// grantKey is a set's key in agentpolicy's engine: its scope and its
// source. A grant made by Engine.Grant or GrantOver joins the policy,
// which no revocation touches and a replacement of the policy ends, and
// is keyed apart (always).
type grantKey struct {
	scope, source string
	always        bool
}

// grants replays the path's verdicts. Each allow a skill's source
// granted makes a grant; an activation replaces its source's set under
// its scope, a revocation ends its source's set under its scope, a
// scope's revocation every set of the scope, and a replacement of the
// policy every plain grant. A record with no grant
// member is read by its reason, as agentpolicy wrote it before
// v0.0.19: there a scope's revocation, which named no scope a grant
// could be matched to, ends every grant. It returns every grant, with
// the index on the path it began and ended at (-1 for none).
func grants(path []agentsession.Entry) (all []Grant, began, ended []int) {
	live := map[grantKey][]int{} // set -> indexes into all
	reads := skillReads(path)
	end := func(i int, v Verdict, match func(grantKey) bool) {
		for k, gs := range live {
			if !match(k) {
				continue
			}
			for _, g := range gs {
				all[g].Ended, all[g].Replaced, all[g].PolicyReplaced, ended[g] = v.Entry, v.Grant == kindActivate, v.Grant == kindReplace, i
			}
			delete(live, k)
		}
	}
	add := func(i int, v Verdict, k grantKey) {
		skill, ok := skillOf(v.Source, reads)
		if !ok {
			return
		}
		all = append(all, Grant{Skill: skill, Source: v.Source, Scope: v.Scope, Rule: v.Rule, Since: v.Entry})
		began, ended = append(began, i), append(ended, -1)
		live[k] = append(live[k], len(all)-1)
	}
	for i, e := range path {
		v, ok := verdictOf(e)
		if !ok {
			continue
		}
		if v.Grant != "" {
			set := grantKey{scope: v.Scope, source: v.Source}
			switch v.Grant {
			case kindActivate, kindRevoke:
				end(i, v, func(k grantKey) bool { return k == set })
			case kindRevokeScope:
				end(i, v, func(k grantKey) bool { return !k.always && k.scope == v.Scope })
			case kindReplace:
				end(i, v, func(k grantKey) bool { return k.always })
			case kindGrantSet:
				// A refused rule is recorded as a block of the set.
				if v.Action == "allow" && v.Rule != "" {
					add(i, v, set)
				}
			case kindGrant, kindGrantOver:
				if v.Action == "allow" && v.Rule != "" {
					add(i, v, grantKey{source: v.Source, always: true})
				}
			}
			continue
		}
		switch {
		case strings.HasPrefix(v.Reason, revokedScopePrefix) || v.Reason == revokedUnscoped:
			end(i, v, func(grantKey) bool { return true })
		case strings.HasPrefix(v.Reason, revokedPrefix):
			src := strings.TrimPrefix(v.Reason, revokedPrefix)
			end(i, v, func(k grantKey) bool { return k.source == src })
		case v.Action == "allow" && v.Rule != "" && strings.HasPrefix(v.Reason, "granted "):
			add(i, v, grantKey{source: v.Source})
		}
	}
	return all, began, ended
}

// grantsAt are the grants in force at path[at]: made before it and not
// revoked before it. A grant revoked later still shows, with Ended set.
func grantsAt(path []agentsession.Entry, at int) []Grant {
	all, began, ended := grants(path)
	var out []Grant
	for i, g := range all {
		if began[i] < at && (ended[i] < 0 || ended[i] > at) {
			out = append(out, g)
		}
	}
	return out
}

// Manifest is the memory manifest in force, as agentmemory's fold keeps
// it: what the model's memory block held, what its bound left out, and
// whether the product left the block out whole (Dropped).
type Manifest struct {
	agentmemory.Manifest
	// Entry is the record entry the manifest in force came from.
	Entry string `json:"-"`
}

// Refusal is a manifest record the fold refused, and why. A refused record
// leaves the manifest in force as it was, as agentmemory's fold does.
// Unresolved says the delta's base is none of the manifests last in
// force (agentmemory.ErrManifestBase); otherwise the record is malformed.
type Refusal struct {
	Entry      string
	Why        string
	Unresolved bool
}

// manifestIn folds the manifest records on the path with agentmemory's
// ManifestFold (RFC 0001 section 8.5) and returns the one in force. ok
// is false when the path holds none. A record the fold refuses is
// listed, so a corrupt or invented delta is never shown as the memory
// the model had.
func manifestIn(path []agentsession.Entry) (m Manifest, refused []Refusal, ok bool) {
	var fold agentmemory.ManifestFold
	for _, e := range path {
		c, isCustom := e.(*agentsession.CustomEntry)
		if !isCustom || c.NS != agentmemory.ManifestNS {
			continue
		}
		if err := fold.Apply(c.Data); err != nil {
			refused = append(refused, Refusal{Entry: c.ID, Why: err.Error(), Unresolved: errors.Is(err, agentmemory.ErrManifestBase)})
			continue
		}
		m, ok = Manifest{Manifest: fold.Manifest(), Entry: c.ID}, true
	}
	return m, refused, ok
}

// elicitation is a question a tool put to the user and what came of it
// (agentturn/session, ElicitationNS).
func elicitationsFor(path []agentsession.Entry, callID string) []session.Elicitation {
	var out []session.Elicitation
	for _, e := range path {
		c, ok := e.(*agentsession.CustomEntry)
		if !ok || c.NS != session.ElicitationNS {
			continue
		}
		var el session.Elicitation
		if json.Unmarshal(c.Data, &el) != nil {
			continue
		}
		if c.CallID == callID || el.Call == callID {
			out = append(out, el)
		}
	}
	return out
}
