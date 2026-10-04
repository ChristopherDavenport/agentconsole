package tui

import (
	"strings"
	"testing"

	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client"
	"github.com/ChristopherDavenport/agentconsole/view"
)

func TestStatusTextShowsRunningCost(t *testing.T) {
	m := view.Model{
		Usage: openresponses.Usage{InputTokens: 300, OutputTokens: 50, TotalTokens: 350},
		UsageByModel: map[string]openresponses.Usage{
			"model": {InputTokens: 300, OutputTokens: 50, TotalTokens: 350},
		},
	}
	cost := client.Cost(func(model string, u openresponses.Usage) (float64, bool) {
		if model != "model" {
			return 0, false
		}
		return (float64(u.InputTokens) + 2*float64(u.OutputTokens)) / 1000, true
	})
	s := statusText(m, false, false, true, cost)
	for _, want := range []string{"tokens 300 in, 50 out", "$0.4000"} {
		if !strings.Contains(s, want) {
			t.Errorf("status lacks %q:\n%s", want, s)
		}
	}
}

func TestStatusTextNamesUnpricedModels(t *testing.T) {
	m := view.Model{
		Usage: openresponses.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		UsageByModel: map[string]openresponses.Usage{
			"provider/model-name": {InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		},
	}
	cost := client.Cost(func(string, openresponses.Usage) (float64, bool) { return 0, false })
	s := statusText(m, false, false, true, cost)
	if !strings.Contains(s, "unpriced: provider/model-name") {
		t.Errorf("status does not name the unpriced model:\n%s", s)
	}
	if strings.Contains(s, "$") {
		t.Errorf("status shows a cost for an unpriced model:\n%s", s)
	}
}
