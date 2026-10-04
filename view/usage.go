package view

import (
	"encoding/json"
	"sort"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client"
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
