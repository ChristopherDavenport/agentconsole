package tui

import (
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/openresponses"
)

// steerQueue keeps the steers typed and not yet handed to the backend.
// Each is written to the record by Control.Steer, off the program's
// goroutine, and one command per steer pops the oldest while it holds
// send, so two typed in quick succession reach the record in the order
// they were typed.
type steerQueue struct {
	send  sync.Mutex
	mu    sync.Mutex
	items []steerItem
}

type steerItem struct {
	text  string
	items []openresponses.Item
}

// steerDoneMsg is a steer handed to the backend; err says it was not
// queued, and text is what was typed.
type steerDoneMsg struct {
	text string
	err  error
}

// steer queues items, typed as text, into the run in a command. What
// waits is shown from the record, once the queued entry Control.Steer
// writes lands.
func (m *Model) steer(text string, items ...openresponses.Item) tea.Cmd {
	q, ctl, ctx := m.steers, m.ctl, m.ctx
	q.mu.Lock()
	q.items = append(q.items, steerItem{text: text, items: items})
	q.mu.Unlock()
	m.inflight.Add(1)
	return func() tea.Msg {
		defer m.inflight.Done()
		q.send.Lock()
		defer q.send.Unlock()
		q.mu.Lock()
		next := q.items[0]
		q.items = q.items[1:]
		q.mu.Unlock()
		if err := ctl.Steer(ctx, next.items...); err != nil {
			return steerDoneMsg{text: next.text, err: err}
		}
		return steerDoneMsg{}
	}
}

// queuedMax is how many waiting inputs are listed over the input; the
// rest are counted.
const queuedMax = 3

// queuedLines lists the inputs the agent accepted that the conversation
// does not hold yet, from the record, one line each: a steer waits for
// the run's next model call, a follow-up for the run's end, and either
// for the next run once none is going.
func (m *Model) queuedLines() []string {
	if !m.typing() || len(m.view.Queued) == 0 {
		return nil
	}
	var lines []string
	for i, q := range m.view.Queued {
		if i == queuedMax {
			lines = append(lines, dimStyle.Render(truncate("  ... "+itoa(len(m.view.Queued)-queuedMax)+" more queued", m.width)))
			break
		}
		label := "queued"
		switch {
		case !m.running():
			label = "queued for the next run"
		case q.Mode == agentsession.ModeFollowUp:
			label = "queued for the end of the run"
		}
		text := "(" + q.Item.ItemType() + ")"
		if msg, ok := q.Item.(*openresponses.Message); ok {
			text = msg.Text()
		}
		text = strings.Join(strings.Fields(text), " ")
		lines = append(lines, dimStyle.Render(truncate("  "+label+": "+text, m.width)))
	}
	return lines
}
