package inspect

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

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
// "agentskill:NAME", or a product's own naming ("skill:NAME" in dex) when
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
	Rule   string
	// Since is the verdict entry that recorded the grant.
	Since string
	// Ended is the verdict entry that revoked the source's grants, "" while
	// they hold on the whole path.
	Ended string
}

// grants replays the path's verdicts: each "granted <rule>" allow of a
// skill's source makes a grant, and the revocation verdict of a source
// ends the ones it holds. It returns every grant, with the index on the
// path it began and ended at (-1 for none).
func grants(path []agentsession.Entry) (all []Grant, began, ended []int) {
	live := map[string][]int{} // source -> indexes into all
	reads := skillReads(path)
	for i, e := range path {
		v, ok := verdictOf(e)
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(v.Reason, revokedScopePrefix) || v.Reason == revokedUnscoped:
			for src, gs := range live {
				for _, g := range gs {
					all[g].Ended, ended[g] = v.Entry, i
				}
				delete(live, src)
			}
		case strings.HasPrefix(v.Reason, revokedPrefix):
			src := strings.TrimPrefix(v.Reason, revokedPrefix)
			for _, g := range live[src] {
				all[g].Ended, ended[g] = v.Entry, i
			}
			delete(live, src)
		case v.Action == "allow" && v.Rule != "" && strings.HasPrefix(v.Reason, "granted "):
			skill, ok := skillOf(v.Source, reads)
			if !ok {
				continue
			}
			all = append(all, Grant{Skill: skill, Source: v.Source, Rule: v.Rule, Since: v.Entry})
			began, ended = append(began, i), append(ended, -1)
			live[v.Source] = append(live[v.Source], len(all)-1)
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

// Manifest is the memory manifest in force: what the model's memory block
// held and what its bound left out.
type Manifest struct {
	Entries []ManifestEntry `json:"entries"`
	Omitted []ManifestEntry `json:"omitted,omitempty"`
	// Entry is the record entry the manifest in force came from.
	Entry string `json:"-"`
}

// ManifestEntry names one memory by scope and name.
type ManifestEntry struct {
	Scope  string `json:"scope"`
	Name   string `json:"name"`
	Hash   string `json:"hash"`
	Bytes  int    `json:"bytes"`
	Reason string `json:"reason,omitempty"`
}

// manifestNS is where agentmemory records its manifest. A record is
// whole, or a delta on one of the manifests last in force: a base (that
// manifest's hash), the result's hash, and per list a run of keeps and
// entries written whole (agentmemory v0.0.10, render.go).
const manifestNS = "agentmemory:render"

// foldDepth is how many distinct manifests a delta's base may be among.
const foldDepth = 8

type manifestDelta struct {
	Base    string       `json:"base"`
	Hash    string       `json:"hash"`
	Entries []manifestOp `json:"entries,omitempty"`
	Omitted []manifestOp `json:"omitted,omitempty"`
}

type manifestOp struct {
	Keep int `json:"keep,omitempty"`
	*ManifestEntry
}

func (o *manifestOp) UnmarshalJSON(data []byte) error {
	var probe struct {
		Keep  int    `json:"keep"`
		Scope string `json:"scope"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	if probe.Keep != 0 && probe.Scope != "" {
		return errors.New("an element is both a keep and an entry")
	}
	if probe.Keep != 0 {
		o.Keep = probe.Keep
		return nil
	}
	var e ManifestEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return err
	}
	o.ManifestEntry = &e
	return nil
}

func manifestHash(m Manifest) string {
	var b strings.Builder
	for _, e := range m.Entries {
		b.WriteString("+ " + e.Scope + "/" + e.Name + " " + e.Hash + " " + strconv.Itoa(e.Bytes) + "\n")
	}
	for _, e := range m.Omitted {
		b.WriteString("- " + e.Scope + "/" + e.Name + " " + e.Hash + " " + strconv.Itoa(e.Bytes) + " " + e.Reason + "\n")
	}
	return sha256Hex(b.String())
}

// applyOps folds a list of ops onto prev, with agentmemory's checks: the
// list in force names no entry twice, an entry names a scope and a name,
// and a keep takes a positive number of the entries left.
func applyOps(prev []ManifestEntry, ops []manifestOp) ([]ManifestEntry, error) {
	at := map[string]int{}
	for i, e := range prev {
		k := e.Scope + "/" + e.Name
		if _, dup := at[k]; dup {
			return nil, errors.New("the manifest in force names an entry twice")
		}
		at[k] = i
	}
	var out []ManifestEntry
	cursor := 0
	for i, op := range ops {
		switch {
		case op.ManifestEntry != nil:
			if op.Scope == "" || op.Name == "" {
				return nil, fmt.Errorf("element %d names no entry", i)
			}
			out = append(out, *op.ManifestEntry)
			if j, ok := at[op.Scope+"/"+op.Name]; ok {
				cursor = j + 1
			}
		case op.Keep > 0 && op.Keep <= len(prev)-cursor:
			out = append(out, prev[cursor:cursor+op.Keep]...)
			cursor += op.Keep
		case op.Keep > 0:
			return nil, fmt.Errorf("element %d keeps %d entries and %d are left", i, op.Keep, len(prev)-cursor)
		default:
			return nil, fmt.Errorf("element %d is neither a positive keep nor an entry", i)
		}
	}
	return out, nil
}

// Refusal is a manifest record the fold refused, and why. A refused record
// leaves the manifest in force as it was, as agentmemory's fold does.
type Refusal struct {
	Entry string
	Why   string
}

// manifestIn folds the manifest records on the path, as agentmemory's
// ManifestFold does, and returns the one in force. ok is false when the
// path holds none. A record that does not fold is refused, with the
// checks agentmemory makes: a delta's base is among the manifests last in
// force, its elements are well formed, and the result hashes to the hash
// the delta states (so a corrupt or invented delta is not shown as the
// memory the model had).
func manifestIn(path []agentsession.Entry) (m Manifest, refused []Refusal, ok bool) {
	var recent []Manifest
	var hashes []string
	push := func(m Manifest, hash string) {
		if i := slices.Index(hashes, hash); i >= 0 {
			recent, hashes = slices.Delete(recent, i, i+1), slices.Delete(hashes, i, i+1)
		}
		recent = append([]Manifest{m}, recent...)
		hashes = append([]string{hash}, hashes...)
		if len(recent) > foldDepth {
			recent, hashes = recent[:foldDepth], hashes[:foldDepth]
		}
	}
	for _, e := range path {
		c, isCustom := e.(*agentsession.CustomEntry)
		if !isCustom || c.NS != manifestNS {
			continue
		}
		refuse := func(format string, a ...any) {
			refused = append(refused, Refusal{Entry: c.ID, Why: fmt.Sprintf(format, a...)})
		}
		var probe struct {
			Base *string `json:"base"`
		}
		if err := json.Unmarshal(c.Data, &probe); err != nil {
			refuse("%v", err)
			continue
		}
		if probe.Base == nil {
			var whole Manifest
			if err := json.Unmarshal(c.Data, &whole); err != nil {
				refuse("%v", err)
				continue
			}
			whole.Entry = c.ID
			push(whole, manifestHash(whole))
			continue
		}
		var d manifestDelta
		if err := json.Unmarshal(c.Data, &d); err != nil {
			refuse("%v", err)
			continue
		}
		var base Manifest
		if i := slices.Index(hashes, d.Base); i >= 0 {
			base = recent[i]
		} else if !(len(recent) == 0 && d.Base == manifestHash(Manifest{})) {
			refuse("the delta is based on %s, which is not among the manifests last in force", d.Base)
			continue
		}
		var out Manifest
		var err error
		if out.Entries, err = applyOps(base.Entries, d.Entries); err != nil {
			refuse("entries: %v", err)
			continue
		}
		if out.Omitted, err = applyOps(base.Omitted, d.Omitted); err != nil {
			refuse("omitted: %v", err)
			continue
		}
		if got := manifestHash(out); got != d.Hash {
			refuse("the delta says %s but the folded result hashes %s", d.Hash, got)
			continue
		}
		out.Entry = c.ID
		push(out, d.Hash)
	}
	if len(recent) == 0 {
		return Manifest{}, refused, false
	}
	return recent[0], refused, true
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
