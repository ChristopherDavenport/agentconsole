package tui_test

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client"
	"github.com/ChristopherDavenport/agentconsole/internal/tui"
)

// ending is a model whose one response ends incomplete for reason, the
// details carrying extra as a provider's adapter adds its own members.
// Its message says text, then refusal as a refusal part, each when set.
type ending struct {
	reason        openresponses.IncompleteReason
	extra         map[string]any
	text, refusal string
}

func (m *ending) CreateStream(_ context.Context, req openresponses.Request, sink openresponses.EventSink) error {
	next := sink
	sink = openresponses.EventSinkFunc(func(ev openresponses.StreamEvent) error {
		if e, ok := ev.(*openresponses.ResponseIncompleteEvent); ok && m.extra != nil {
			e.Response.IncompleteDetails.Extra = m.extra
		}
		return next.Send(ev)
	})
	em := openresponses.NewEmitter(sink, openresponses.NewResponse(req))
	w, err := em.Message(openresponses.PhaseFinalAnswer)
	if err != nil {
		return err
	}
	if m.text != "" {
		if err := w.Text(m.text); err != nil {
			return err
		}
	}
	if m.refusal != "" {
		if err := w.Refusal(m.refusal); err != nil {
			return err
		}
	}
	if err := w.Close(); err != nil {
		return err
	}
	return em.Incomplete(m.reason)
}

// TestARefusalIsShownWithItsCategory (#54): a model's refusal part is
// in the transcript, marked as one, and a run the provider's content
// filter stopped says so under it with the provider's category.
func TestARefusalIsShownWithItsCategory(t *testing.T) {
	a := newApp(t, agentturn.Config{ModelName: "scripted", Model: &ending{
		reason:  openresponses.IncompleteReasonContentFilter,
		extra:   map[string]any{"anthropic.refusal_category": "cyber"},
		refusal: "I can't help with that.",
	}})
	a.submit("go")
	a.waitFor("the refusal and the end", all(
		has("refusal: I can't help with that.", "the provider refused: content filter (anthropic.refusal_category: cyber)", "idle"),
		lacks("(not committed yet)")))
}

// TestACutAnswerSaysItWasCut (#54): an answer the output limit cut is
// shown, and the run's end under it says it was cut.
func TestACutAnswerSaysItWasCut(t *testing.T) {
	a := newApp(t, agentturn.Config{ModelName: "scripted", Model: &ending{
		reason: openresponses.IncompleteReasonMaxOutputTokens,
		text:   "Once upon a time",
	}})
	a.submit("go")
	a.waitFor("the cut answer and the end", all(
		has("Once upon a time", "the answer was cut at the model's output limit", "idle"),
		lacks("refused", "(not committed yet)")))
}

// reopen is a second client on a's session, attached after its runs
// ended: it has none of their live events and reads them from the
// record, as a reopened session or a viewer that attaches late does.
func (a *app) reopen() *app {
	a.t.Helper()
	ctx, cancel := context.WithCancel(a.ctx)
	b := &app{t: a.t, ctx: ctx, cancel: cancel, store: a.store, rec: a.rec, ctl: a.ctl, record: a.record}
	b.m = tui.New(ctx, a.record, a.ctl, client.Direct(a.ctl), tui.WithPanelSettle(0))
	b.wait = tui.Attach(ctx, a.record, a.ctl, b.send)
	a.t.Cleanup(func() {
		cancel()
		b.wait()
		b.wg.Wait()
	})
	b.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	return b
}

// TestAReopenedSessionShowsHowTheRunEnded (#54): the end of a run the
// content filter stopped, with its category, and of one the output
// limit cut, are read from the record, so a client that never saw the
// live end shows them too.
func TestAReopenedSessionShowsHowTheRunEnded(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model *ending
		want  []string
	}{
		{"refused", &ending{reason: openresponses.IncompleteReasonContentFilter,
			extra: map[string]any{"anthropic.refusal_category": "cyber"}, refusal: "I can't help with that."},
			[]string{"refusal: I can't help with that.", "the provider refused: content filter (anthropic.refusal_category: cyber)", "last run stopped: the provider refused"}},
		{"cut", &ending{reason: openresponses.IncompleteReasonMaxOutputTokens, text: "Once upon a time"},
			[]string{"Once upon a time", "the answer was cut at the model's output limit", "last run stopped: answer cut at the output limit"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newApp(t, agentturn.Config{ModelName: "scripted", Model: tc.model})
			a.submit("go")
			a.waitFor("the end live", all(has(tc.want...), lacks("(not committed yet)")))
			b := a.reopen()
			b.waitFor("the end from the record", all(has(append([]string{"idle"}, tc.want...)...), lacks("(not committed yet)")))
		})
	}
}
