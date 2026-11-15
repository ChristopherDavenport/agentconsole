package view

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client"
	"github.com/ChristopherDavenport/agentconsole/internal/incomplete"
)

// Usage sums the token usage of every model call on a path, split by the
// model each call was made under. It counts responses and the folds,
// failed or not.
func Usage(path []agentsession.Entry) (openresponses.Usage, map[string]openresponses.Usage) {
	var total openresponses.Usage
	byModel := map[string]openresponses.Usage{}
	model := ""
	for _, e := range path {
		var u *openresponses.Usage
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
			u = v.Usage
			if v.Model != "" {
				m = v.Model
			}
		case *agentsession.CompactionEntry:
			model = v.Config.Model
			u, m = v.Usage, v.Config.Model
		case *agentsession.BranchSummaryEntry:
			u = v.Usage
		case *agentsession.CustomEntry:
			if v.NS != session.FailedFoldNS {
				continue
			}
			var f session.FailedFold
			if json.Unmarshal(v.Data, &f) != nil {
				continue
			}
			u = f.Usage
			if f.Model != "" {
				m = f.Model
			}
		default:
			continue
		}
		if u == nil {
			continue
		}
		total.InputTokens += u.InputTokens
		total.OutputTokens += u.OutputTokens
		total.TotalTokens += u.TotalTokens
		total.InputTokensDetails.CachedTokens += u.InputTokensDetails.CachedTokens
		total.OutputTokensDetails.ReasoningTokens += u.OutputTokensDetails.ReasoningTokens
		b := byModel[m]
		b.InputTokens += u.InputTokens
		b.OutputTokens += u.OutputTokens
		b.TotalTokens += u.TotalTokens
		b.InputTokensDetails.CachedTokens += u.InputTokensDetails.CachedTokens
		b.OutputTokensDetails.ReasoningTokens += u.OutputTokensDetails.ReasoningTokens
		byModel[m] = b
	}
	return total, byModel
}

// Run is a run on a line, by the record: when its start entry and its
// end entry were written, and the token use of its model calls so far.
// A client shows it as the current turn's figures.
type Run struct {
	ID      string
	Started time.Time
	// Ended is zero while the line holds no end entry for the run.
	Ended time.Time
	Usage openresponses.Usage
	// Cause is what stopped the run, by its end entry: the cause an end
	// written stopped names in its ref, or one written aborted after
	// its last response ended incomplete (agentturn v0.0.40: the
	// provider's content filter, the output limit, or a stop after a
	// response the limit cut; ref "incomplete" reads as
	// max_output_tokens). It is "" with no end entry and for any other
	// end, a guard's stop included.
	Cause agentturn.StopCause
	// Incomplete is the incomplete details of the run's last response
	// when it ended incomplete and the run has an end entry, extension
	// members such as anthropic.refusal_category in Extra, and nil
	// otherwise: the record's [agentturn.RunEnd.Incomplete].
	Incomplete *openresponses.IncompleteDetails
}

// recordedCause is the stop cause a run's end entry names, as agentturn
// reads it back: a stopped end's ref, before any ":", and an aborted
// end's when it names a stop cause other than a guard's, which is
// written with its error.
func recordedCause(end *agentsession.RunEntry) agentturn.StopCause {
	cause, _, _ := strings.Cut(end.Ref, ":")
	switch end.Reason {
	case agentsession.ReasonStopped:
		return agentturn.StopCause(cause)
	case agentsession.ReasonAborted:
		switch c := agentturn.StopCause(cause); c {
		case agentturn.StopMaxTurns, agentturn.StopHook, agentturn.StopTerminate, agentturn.StopPartialTerminate,
			agentturn.StopRefused, agentturn.StopContentFilter, agentturn.StopMaxOutputTokens:
			return c
		case "incomplete":
			return agentturn.StopMaxOutputTokens
		}
	}
	return ""
}

// lastRun is the last run whose start entry is on path, zero when there
// is none. Its usage is the model calls' after the start entry, counted
// as [Usage] counts them.
func lastRun(path []agentsession.Entry) Run {
	for i := len(path) - 1; i >= 0; i-- {
		start, ok := path[i].(*agentsession.RunEntry)
		if !ok || !start.IsStart() {
			continue
		}
		r := Run{ID: start.RunID, Started: start.Timestamp}
		r.Usage, _ = Usage(path[i+1:])
		var last *agentsession.ResponseEntry
		for _, e := range path[i+1:] {
			switch x := e.(type) {
			case *agentsession.ResponseEntry:
				last = x
			case *agentsession.RunEntry:
				if x.IsEnd() && x.RunID == start.RunID {
					r.Ended, r.Cause = x.Timestamp, recordedCause(x)
					if last != nil {
						r.Incomplete = incomplete.Copy(last.Incomplete)
					}
					return r
				}
			}
		}
		return r
	}
	return Run{}
}

// worked sums how long the runs on path took, each from its start entry
// to its end entry. A run with no end on the path (the one going, or one
// whose process died) adds nothing.
func worked(path []agentsession.Entry) time.Duration {
	var total time.Duration
	started := map[string]time.Time{}
	for _, e := range path {
		r, ok := e.(*agentsession.RunEntry)
		switch {
		case !ok:
		case r.IsStart():
			started[r.RunID] = r.Timestamp
		case r.IsEnd():
			if t, ok := started[r.RunID]; ok && !t.IsZero() && !r.Timestamp.IsZero() {
				total += max(r.Timestamp.Sub(t), 0)
				delete(started, r.RunID)
			}
		}
	}
	return total
}

type PriceSummary struct {
	// Total is the line's cost in US dollars, valid when Priced is true.
	Total float64
	// Priced says every model with usage on the line was priced.
	Priced bool
	// Unpriced lists the models the cost source has no price for, sorted.
	Unpriced []string
}

// Price prices one model call under cost. Cost hooks are linear over the
// usage they are given (the price table's are), so each model's aggregate
// usage is priced once. A nil cost leaves the line unpriced and unnamed.
func Price(byModel map[string]openresponses.Usage, cost client.Cost) PriceSummary {
	if cost == nil {
		return PriceSummary{}
	}
	models := make([]string, 0, len(byModel))
	for m := range byModel {
		models = append(models, m)
	}
	sort.Strings(models)
	p := PriceSummary{Priced: true}
	for _, m := range models {
		usd, priced := cost(m, byModel[m])
		if !priced {
			p.Priced = false
			p.Unpriced = append(p.Unpriced, labelModel(m))
			continue
		}
		if p.Priced {
			p.Total += usd
		}
	}
	return p
}

func labelModel(m string) string {
	if m == "" {
		return "(unknown)"
	}
	return m
}

// ModelCall is a model call on a line, by the record: the model it was
// made under and its token usage. A client reads the context's size off
// the last one, whose input tokens are how much the model was sent.
type ModelCall struct {
	Model string
	Usage openresponses.Usage
}

// lastCall is the last response on path that reports its usage, with
// the model it was made under: the response's own, or the config's in
// force there. A compaction or a branch summary after it replaces the
// context, whose size is then unknown until the next call: zero.
func lastCall(path []agentsession.Entry) ModelCall {
	var last ModelCall
	model := ""
	for _, e := range path {
		switch v := e.(type) {
		case *agentsession.ConfigEntry:
			if v.Replace {
				model = ""
			}
			if v.Model != "" {
				model = v.Model
			}
		case *agentsession.CompactionEntry:
			model = v.Config.Model
			last = ModelCall{}
		case *agentsession.BranchSummaryEntry:
			last = ModelCall{}
		case *agentsession.ResponseEntry:
			if v.Usage == nil {
				continue
			}
			last = ModelCall{Model: model, Usage: *v.Usage}
			if v.Model != "" {
				last.Model = v.Model
			}
		}
	}
	return last
}
