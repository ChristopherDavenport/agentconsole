package tui_test

import (
	"context"
	"testing"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/internal/tui"
)

// recordedUnder is a session whose one run was made under model, as a
// session the host opens after a relaunch is.
func recordedUnder(model string) starter {
	return func(ctx context.Context, store agentsession.Store) (*session.Recorder, error) {
		rec, _, err := session.Start(ctx, store, agentsession.Header{Records: agentsession.AllRecords})
		if err != nil {
			return nil, err
		}
		cfg := cfgWith(say(nil, nil, "answered under "+model))
		cfg.ModelName = model
		ag := agentturn.New(cfg)
		detach := rec.Attach(ag)
		defer detach()
		if _, err := ag.Prompt(ctx, openresponses.UserText("before the relaunch")); err != nil {
			return nil, err
		}
		return rec, nil
	}
}

// The model the host configured shows before the first run after a
// relaunch, with the record's beside it until a run under the host's
// lands; then only the host's (#7).
func TestTheHostsModelShowsBeforeTheFirstRun(t *testing.T) {
	cfg := cfgWith(say(nil, nil, "answered under b"))
	cfg.ModelName = "model-b"
	a := newAppWith(t, cfg, recordedUnder("model-a"), nil, tui.WithStatus(tui.StatusConfig{
		Model: func() string { return "model-b" },
	}))
	a.waitFor("the host's model with the record's", has("answered under model-a", "model model-b (record: model-a until the next run)"))
	a.submit("after the relaunch")
	a.waitFor("the host's model alone", all(has("answered under b", "model model-b", "idle"), lacks("record:")))
}
