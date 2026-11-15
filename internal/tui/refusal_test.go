package tui_test

import (
	"context"
	"testing"

	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"
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
