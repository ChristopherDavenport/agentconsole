package tui_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/internal/tui"
)

// harness is a submit function that plays the program running the
// client: a line starting with a slash is its own command, answered with
// a reply, and anything else goes to the agent.
func harness(seen *[]tui.Line, mu *sync.Mutex) func(context.Context, tui.Line) (tui.Submission, error) {
	return func(_ context.Context, l tui.Line) (tui.Submission, error) {
		mu.Lock()
		*seen = append(*seen, l)
		mu.Unlock()
		switch {
		case strings.HasPrefix(l.Text, "/fail"):
			return tui.Submission{}, errors.New("no such command")
		case strings.HasPrefix(l.Text, "/"):
			return tui.Submission{Reply: "harness ran " + l.Text}, nil
		case l.Running:
			return tui.Submission{Steer: []openresponses.Item{openresponses.UserText(l.Text)}}, nil
		}
		return tui.Submission{Prompt: []openresponses.Item{openresponses.UserText(l.Text)}}, nil
	}
}

func TestASubmitFunctionDecidesWhatALineIs(t *testing.T) {
	var seen []tui.Line
	var mu sync.Mutex
	a := newAppWith(t, cfgWith(say(nil, nil, "hello back")), nil, nil, tui.WithSubmit(harness(&seen, &mu)))

	a.submit("/model other")
	a.waitFor("the harness's reply", all(has("harness ran /model other"), lacks("hello back")))
	if s := a.screen(); prompted("/model other")(s) {
		t.Errorf("the command reached the agent:\n%s", s)
	}

	a.submit("hi")
	a.waitFor("the prompt answered, the reply gone", all(prompted("hi"), has("hello back", "idle"), lacks("harness ran")))
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0].Text != "/model other" || seen[1].Text != "hi" || seen[0].Running || seen[1].Running {
		t.Errorf("the submit function saw %+v", seen)
	}
}

func TestASubmitFunctionSteersARunThatGoes(t *testing.T) {
	g := newGates()
	var seen []tui.Line
	var mu sync.Mutex
	a := newAppWith(t, cfgWith(say(g, map[int]string{0: "mid"}, "first"), say(nil, nil, "second")), nil, nil, tui.WithSubmit(harness(&seen, &mu)))
	t.Cleanup(func() { g.release("mid") })

	a.submit("start")
	g.arrive(t, "mid")
	a.waitFor("writing", has("writing"))
	a.submit("/status")
	a.waitFor("the command answered mid-run", has("harness ran /status"))
	a.submit("also this")
	// The submit function is a hop before the steer: let it land in the
	// run before the run can end.
	a.waitFor("the steer queued", has("queued: also this"))
	g.release("mid")
	s := a.waitFor("the steer on the record and answered", all(has("also this", "second", "idle")))
	if n := strings.Count(s, "also this"); n != 1 {
		t.Errorf("steer shown %d times, want 1:\n%s", n, s)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 || seen[0].Running || !seen[1].Running || !seen[2].Running {
		t.Errorf("the submit function saw %+v", seen)
	}
}

func TestASubmitErrorIsShownAndTheLineGivenBack(t *testing.T) {
	var seen []tui.Line
	var mu sync.Mutex
	a := newAppWith(t, cfgWith(say(nil, nil, "unused")), nil, nil, tui.WithSubmit(harness(&seen, &mu)))

	a.submit("/fail now")
	a.waitFor("the error and the line back in the input", has("submit: no such command", "> /fail now"))
}

func TestAPromptAndASteerAtOnceIsRefused(t *testing.T) {
	both := func(context.Context, tui.Line) (tui.Submission, error) {
		item := openresponses.UserText("x")
		return tui.Submission{Prompt: []openresponses.Item{item}, Steer: []openresponses.Item{item}}, nil
	}
	a := newAppWith(t, cfgWith(say(nil, nil, "unused")), nil, nil, tui.WithSubmit(both))

	a.submit("twice")
	s := a.waitFor("the refusal", has("submit: both a prompt and a steer", "> twice"))
	if prompted("x")(s) || strings.Contains(s, "unused") {
		t.Errorf("the line reached the agent:\n%s", s)
	}
}

func TestLinesWaitForTheSubmitFunctionInOrder(t *testing.T) {
	g := newGates()
	t.Cleanup(func() { g.release("first") })
	var order []string
	var mu sync.Mutex
	slow := func(ctx context.Context, l tui.Line) (tui.Submission, error) {
		if l.Text == "/first" {
			if err := g.hold(ctx, "first"); err != nil {
				return tui.Submission{}, err
			}
		}
		mu.Lock()
		order = append(order, l.Text)
		mu.Unlock()
		return tui.Submission{Reply: "done " + l.Text}, nil
	}
	a := newAppWith(t, cfgWith(say(nil, nil, "unused")), nil, nil, tui.WithSubmit(slow))

	a.submit("/first")
	g.arrive(t, "first")
	a.submit("/second")
	a.waitFor("the first sending and the second waiting", has("sending: /first", "waiting: /second"))
	g.release("first")
	a.waitFor("the second's reply", all(has("done /second"), lacks("sending:", "waiting:", "done /first")))
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(order, ",") != "/first,/second" {
		t.Errorf("handled in the order %v", order)
	}
}

func TestALongReplyIsCutToItsRows(t *testing.T) {
	var lines []string
	for i := range 40 {
		lines = append(lines, "row "+strconv.Itoa(i))
	}
	long := func(context.Context, tui.Line) (tui.Submission, error) {
		return tui.Submission{Reply: strings.Join(lines, "\n") + "\n"}, nil
	}
	a := newAppWith(t, cfgWith(say(nil, nil, "unused")), nil, nil, tui.WithSubmit(long))

	a.submit("/tools")
	// The screen is 30 rows, so a third of it: 9 rows of the reply and a
	// tenth that counts the rest.
	s := a.waitFor("the reply cut short", has("row 0", "row 8", "... 31 more lines"))
	if strings.Contains(s, "row 9") {
		t.Errorf("the reply took more than its rows:\n%s", s)
	}
}

func TestAPromptWhileARunGoesIsRefused(t *testing.T) {
	g := newGates()
	t.Cleanup(func() { g.release("mid") })
	prompt := func(_ context.Context, l tui.Line) (tui.Submission, error) {
		return tui.Submission{Prompt: []openresponses.Item{openresponses.UserText(l.Text)}}, nil
	}
	a := newAppWith(t, cfgWith(say(g, map[int]string{0: "mid"}, "first"), say(nil, nil, "unused")), nil, nil, tui.WithSubmit(prompt))

	a.submit("start")
	g.arrive(t, "mid")
	a.waitFor("writing", has("writing"))
	a.submit("again")
	a.waitFor("the refusal, the run still going", has("submit: a prompt while a run goes", "> again", "writing"))
	g.release("mid")
	s := a.waitFor("the run's end", has("first", "idle"))
	if prompted("again")(s) || strings.Contains(s, "unused") {
		t.Errorf("the second prompt reached the agent:\n%s", s)
	}
}
