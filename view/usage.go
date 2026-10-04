package view

import (
	"encoding/json"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"
)

// pathUsage sums the token usage of every model call on a path, split by
// the model each call was made under. It counts responses and the folds,
// failed or not.
func pathUsage(path []agentsession.Entry) (openresponses.Usage, map[string]openresponses.Usage) {
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
		addUsage(&total, u)
		b := byModel[m]
		addUsage(&b, u)
		byModel[m] = b
	}
	return total, byModel
}

func addUsage(dst *openresponses.Usage, src *openresponses.Usage) {
	dst.InputTokens += src.InputTokens
	dst.OutputTokens += src.OutputTokens
	dst.TotalTokens += src.TotalTokens
	dst.InputTokensDetails.CachedTokens += src.InputTokensDetails.CachedTokens
	dst.OutputTokensDetails.ReasoningTokens += src.OutputTokensDetails.ReasoningTokens
}
