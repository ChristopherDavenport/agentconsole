package tui

import (
	"context"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ChristopherDavenport/agentconsole/internal/client"
	"github.com/ChristopherDavenport/agentconsole/internal/view"
)

// ModelMsg carries what the view renders after one more step of the
// record or the live stream. The feed sends one per step, in the order
// the steps were applied.
type ModelMsg struct{ Model view.Model }

// FeedErrMsg reports that a stream of the backend failed. The feed of
// that stream has stopped.
type FeedErrMsg struct {
	Stream string
	Err    error
}

// Attach follows the backend's record and live stream into a
// [view.View] and delivers each resulting [view.Model] through send, as
// a [ModelMsg]; with a program, send is its Send method.
//
// The view is not safe for concurrent use, and the two streams arrive on
// goroutines of their own, so every step is applied under one lock, and
// the model is taken and sent inside it: the program sees the models in
// the order the steps were applied. It cannot be applied from the
// program's goroutine instead. A record change's session is valid only
// until the follow takes its next step, so the change has to be applied
// before the iterator is resumed, and the program runs behind it.
//
// Live subscribes before Attach returns, so a run started right after
// misses no event. The returned function waits for both goroutines,
// which stop when ctx is done.
func Attach(ctx context.Context, be client.Backend, send func(tea.Msg)) (wait func()) {
	var (
		mu sync.Mutex
		v  = view.New()
		wg sync.WaitGroup
	)
	step := func(apply func()) {
		mu.Lock()
		defer mu.Unlock()
		apply()
		send(ModelMsg{Model: v.Model()})
	}
	live := be.Live(ctx)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for ev, err := range live {
			if err != nil {
				if ctx.Err() == nil {
					send(FeedErrMsg{Stream: "live", Err: err})
				}
				return
			}
			step(func() { v.Live(ev) })
		}
	}()
	go func() {
		defer wg.Done()
		for ch, err := range be.Record().Follow(ctx, "") {
			if err != nil {
				if ctx.Err() == nil {
					send(FeedErrMsg{Stream: "record", Err: err})
				}
				return
			}
			step(func() { v.Record(ch) })
		}
	}()
	return wg.Wait
}
