package inspect

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"
)

// Session is the session-level summary pane.
type Session struct {
	ID            string
	Name          string
	CWD           string
	Harness       string
	Format        string
	Created       time.Time
	ParentSession string
	Base          string
	SpawnedBy     string
	// Entries is the session's entry count, Path the length of the viewed
	// line.
	Entries int
	Path    int

	// Verify is the tally over the responses on the viewed line.
	Verify PathVerify
	// Usage is the token usage of the model calls on the viewed line,
	// responses and folds.
	Usage openresponses.Usage
	// UsageByModel is Usage split by the model each call was made under.
	UsageByModel map[string]openresponses.Usage
	// Cost is the viewed line's cost in US dollars under the cost source,
	// and Priced says every call was priced (false with no source).
	Cost   float64
	Priced bool
	// Unpriced lists the models that have usage on the viewed line but
	// that the cost source has no price for, sorted.
	Unpriced []string
	// Config is what the record has in force at the end of the line.
	Config Config
	// Refs are the refs that point at the session, RefsErr why they could
	// not be listed (a store without refs).
	Refs    []agentsession.Ref
	RefsErr string
	// Manifest is the memory manifest in force, nil when the line holds
	// none; ManifestRefused lists the records the fold refused, which
	// leave the manifest in force as it was.
	Manifest        *Manifest
	ManifestRefused []Refusal
	// Grants are the skill grants in force at the end of the line, and
	// those a revocation ended.
	Grants []Grant
}

// PathVerify is the result of checking every response on a line.
type PathVerify struct {
	Responses  int
	Verified   int
	Unhashed   int
	Mismatched int
	Failed     int
	// Problems lists what did not verify, response by response, the
	// mismatches first.
	Problems []Problem
}

// Problem is one response that did not verify.
type Problem struct {
	Response string
	Verify   Verify
}

// OK reports whether every response on the line verified or recorded no
// hash for a reason it names; a mismatch or a failure is not OK.
func (p PathVerify) OK() bool { return p.Mismatched == 0 && p.Failed == 0 }

// Config is the settings in force at the end of the line.
type Config struct {
	Model string
	// InstructionsParts is the number of parts the instructions are
	// built from, and InstructionsLen their length in runes.
	InstructionsParts int
	InstructionsLen   int
	Tools             int
	// Known is false when the context at the end of the line could not be
	// built, with Err saying why.
	Known bool
	Err   string
}

// Session computes the session pane for the line ending at tail.
func (in *Inspector) Session(ctx context.Context, sessionID, tail string, entries int) (Session, error) {
	s, err := in.snapshot(ctx, sessionID, entries)
	if err != nil {
		return Session{}, err
	}
	if tail == "" {
		tail = s.Leaf()
	}
	path := s.Path(tail)
	h := s.Header()
	out := Session{
		ID: h.ID, Name: s.Name(), CWD: h.CWD, Format: s.DeclaredFormat(), Created: h.CreatedAt,
		ParentSession: h.ParentSession, Base: h.Base, SpawnedBy: h.SpawnedBy,
		Entries: s.Len(), Path: len(path),
	}
	if h.Harness != nil {
		out.Harness = h.Harness.Name
		if h.Harness.Version != "" {
			out.Harness += " " + h.Harness.Version
		}
	}
	for i, e := range path {
		if _, ok := e.(*agentsession.ResponseEntry); !ok {
			continue
		}
		v := in.verifyResponse(s, path, i)
		out.Verify.Responses++
		switch v.State {
		case Verified:
			out.Verify.Verified++
		case Unhashed:
			out.Verify.Unhashed++
		case Mismatch:
			out.Verify.Mismatched++
		default:
			out.Verify.Failed++
		}
		if v.State != Verified {
			out.Verify.Problems = append(out.Verify.Problems, Problem{Response: e.Base().ID, Verify: v})
		}
	}
	rank := func(s VerifyState) int {
		switch s {
		case Mismatch:
			return 0
		case Failed:
			return 1
		}
		return 2
	}
	sort.SliceStable(out.Verify.Problems, func(i, j int) bool {
		return rank(out.Verify.Problems[i].Verify.State) < rank(out.Verify.Problems[j].Verify.State)
	})
	if len(path) > 0 {
		if cx, err := s.ContextAt(tail); err != nil {
			out.Config = Config{Err: err.Error()}
		} else {
			set := cx.Settings
			out.Config = Config{Known: true, Model: set.Model, InstructionsParts: len(set.InstructionsParts),
				InstructionsLen: len([]rune(set.Instructions)), Tools: len(set.Tools)}
		}
	}
	if refs, err := in.src.Refs(ctx, sessionID); err != nil {
		if errors.Is(err, agentsession.ErrNoRefs) {
			out.RefsErr = "the store keeps no refs"
		} else {
			out.RefsErr = err.Error()
		}
	} else {
		out.Refs = refs
	}
	m, refused, ok := manifestIn(path)
	if ok {
		out.Manifest = &m
	}
	out.ManifestRefused = refused
	all, _, _ := grants(path)
	out.Grants = all
	in.usage(&out, path)
	return out, nil
}

// usage sums the token usage of every model call on the path, and prices
// it when a cost source was given and every call was priced.
func (in *Inspector) usage(out *Session, path []agentsession.Entry) {
	var u openresponses.Usage
	byModel := map[string]openresponses.Usage{}
	cost := 0.0
	priced := in.cost != nil
	unpriced := map[string]bool{}
	model := ""
	for _, e := range path {
		var eu *openresponses.Usage
		m := model
		switch v := e.(type) {
		case *agentsession.ConfigEntry:
			if v.Replace {
				model = ""
			}
			if v.Model != "" {
				model = v.Model
			}
			continue
		case *agentsession.ResponseEntry:
			eu = v.Usage
			if v.Model != "" {
				m = v.Model
			}
		case *agentsession.CompactionEntry:
			model = v.Config.Model
			eu, m = v.Usage, v.Config.Model
		case *agentsession.BranchSummaryEntry:
			eu = v.Usage
		case *agentsession.CustomEntry:
			// A fold that failed still made its summary calls, and paid
			// for them; the entry holds their usage summed. It leaves the
			// model in force as it was.
			if v.NS != session.FailedFoldNS {
				continue
			}
			var f session.FailedFold
			if json.Unmarshal(v.Data, &f) != nil {
				continue
			}
			eu = f.Usage
			if f.Model != "" {
				m = f.Model
			}
		default:
			continue
		}
		if eu == nil {
			continue
		}
		addUsage(&u, eu)
		b := byModel[m]
		addUsage(&b, eu)
		byModel[m] = b
		if in.cost != nil {
			usd, ok := in.cost(m, *eu)
			if !ok {
				priced = false
				unpriced[m] = true
			} else if priced {
				cost += usd
			}
		}
	}
	out.Usage = u
	out.UsageByModel = byModel
	out.Unpriced = sortedModelNames(unpriced)
	if priced {
		out.Cost, out.Priced = cost, true
	}
}

// sortedModelNames returns the keys of set in order.
func sortedModelNames(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// addUsage adds src's counts into dst.
func addUsage(dst *openresponses.Usage, src *openresponses.Usage) {
	dst.InputTokens += src.InputTokens
	dst.OutputTokens += src.OutputTokens
	dst.TotalTokens += src.TotalTokens
	dst.InputTokensDetails.CachedTokens += src.InputTokensDetails.CachedTokens
	dst.OutputTokensDetails.ReasoningTokens += src.OutputTokensDetails.ReasoningTokens
}
