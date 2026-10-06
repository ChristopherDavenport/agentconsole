package tui_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client"
)

// TestASteerIsShownQueuedUntilTheRunTakesIt steers twice while a tool is
// held, when the run emits nothing. Both are listed over the input at
// once, in the order typed, and leave the list for the conversation when
// the run takes them, each shown once.
func TestASteerIsShownQueuedUntilTheRunTakesIt(t *testing.T) {
	g := newGates()
	cfg := cfgWith(callTool("call_1", "upper", `{"text":"abc"}`), say(nil, nil, "done"))
	cfg.Tools = []agenttool.Tool{upperTool(g)}
	a := newApp(t, cfg)
	t.Cleanup(func() { g.release("tool") })

	a.submit("start")
	g.arrive(t, "tool")
	a.submit("also this")
	a.submit("and that")
	s := a.waitFor("both steers queued", has("queued: also this", "queued: and that"))
	if strings.Index(s, "queued: also this") > strings.Index(s, "queued: and that") {
		t.Errorf("the steers are listed out of order:\n%s", s)
	}
	if !prompted("start")(s) || strings.Count(s, "also this") != 1 {
		t.Errorf("a queued steer is in the conversation before the run took it:\n%s", s)
	}

	g.release("tool")
	s = a.waitFor("the steers taken", all(has("done", "idle"), lacks("queued")))
	for _, steer := range []string{"also this", "and that"} {
		if n := strings.Count(s, steer); n != 1 {
			t.Errorf("%q shown %d times, want 1:\n%s", steer, n, s)
		}
	}
}

// TestAnInputQueuedWhileIdleWaitsForTheNextRun queues an input with no run
// going: it is listed as waiting for the next run, which takes it after
// its prompt.
func TestAnInputQueuedWhileIdleWaitsForTheNextRun(t *testing.T) {
	a := newApp(t, cfgWith(say(nil, nil, "ok")))
	if err := a.be.Control().Steer(context.Background(), openresponses.UserText("later")); err != nil {
		t.Fatal(err)
	}
	a.waitFor("the input queued for the next run", has("queued for the next run: later", "idle"))
	a.submit("go")
	s := a.waitFor("the input taken", all(has("ok", "idle"), lacks("queued")))
	if n := strings.Count(s, "later"); n != 1 {
		t.Errorf("the input is shown %d times, want 1:\n%s", n, s)
	}
}

// failingSteer is a backend whose Steer cannot write the queued entry.
type failingSteer struct{ client.Backend }

func (b failingSteer) Control() client.Control { return failingControl{b.Backend.Control()} }

type failingControl struct{ client.Control }

func (failingControl) Steer(context.Context, ...openresponses.Item) error {
	return errors.New("disk full")
}

// TestASteerThatFailsSaysSoAndGivesTheTextBack: a steer the backend could
// not queue is not listed, the error is shown and the text is back in the
// input to send again.
func TestASteerThatFailsSaysSoAndGivesTheTextBack(t *testing.T) {
	g := newGates()
	a := newAppWith(t, cfgWith(say(g, map[int]string{0: "mid"}, "first")), nil,
		func(be client.Backend) client.Backend { return failingSteer{be} })
	t.Cleanup(func() { g.release("mid") })

	a.submit("start")
	g.arrive(t, "mid")
	a.submit("also this")
	s := a.waitFor("the error", has("steer: disk full", "> also this"))
	if strings.Contains(s, "queued") {
		t.Errorf("a steer that failed is listed:\n%s", s)
	}
}
