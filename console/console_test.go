package console_test

import (
	"context"
	"encoding/base64"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentsession/jsonl"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client/native"
	"github.com/ChristopherDavenport/agentconsole/console"
	"github.com/ChristopherDavenport/agentconsole/internal/scripted"
	"github.com/ChristopherDavenport/agentconsole/internal/termtest"
)

// rig is an agent, its recorder and a backend over a store, and a
// console.Run on a pipe.
type rig struct {
	t     *testing.T
	agent *agentturn.Agent
	be    *native.Backend
	in    *io.PipeWriter
	out   *termtest.Term
	done  chan error
}

func start(t *testing.T, m *scripted.Model, opts ...console.Option) *rig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store, err := jsonl.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	rec, _, err := session.Start(ctx, store, agentsession.Header{Records: agentsession.AllRecords})
	if err != nil {
		t.Fatal(err)
	}
	ag := agentturn.New(agentturn.Config{Model: m, ModelName: "scripted"})
	t.Cleanup(rec.Attach(ag))
	be, err := native.New(ag, rec)
	if err != nil {
		t.Fatal(err)
	}
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	r := &rig{t: t, agent: ag, be: be, in: pw, out: termtest.New(80, 20), done: make(chan error, 1)}
	t.Cleanup(func() { r.out.Close() })
	opts = append([]console.Option{console.WithInput(pr), console.WithOutput(r.out), console.WithoutSignalHandler(), console.WithWindowSize(80, 20)}, opts...)
	go func() { r.done <- console.Run(ctx, be, opts...) }()
	return r
}

func (r *rig) type_(s string) {
	r.t.Helper()
	if _, err := r.in.Write([]byte(s)); err != nil {
		r.t.Fatal(err)
	}
}

// waitScreen waits until the terminal's screen shows sub.
func (r *rig) waitScreen(sub string) {
	r.t.Helper()
	r.waitFor(sub, r.out.Screen)
}

// waitRaw waits until the program has written sub, a command to the
// terminal that the screen does not show.
func (r *rig) waitRaw(sub string) {
	r.t.Helper()
	r.waitFor(sub, r.out.Raw)
}

func (r *rig) waitFor(sub string, read func() string) {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(read(), sub) {
		if time.Now().After(deadline) {
			r.t.Fatalf("%q never written; screen:\n%s", sub, r.out.Screen())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (r *rig) waitEnd() error {
	r.t.Helper()
	select {
	case err := <-r.done:
		return err
	case <-time.After(15 * time.Second):
		r.t.Fatal("Run did not return")
		return nil
	}
}

// TestRunDrivesAnAgentOverAPipe: a prompt typed as a terminal sends it,
// the reply streams through Attach into the program, and ctrl+c with the
// agent idle ends Run cleanly.
func TestRunDrivesAnAgentOverAPipe(t *testing.T) {
	r := start(t, scripted.New(scripted.Say("po", "ng")))
	r.type_("ping\r")
	r.waitScreen("pong")
	for r.agent.State().Running {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	r.type_("\x03")
	if err := r.waitEnd(); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestRunEndsARunItQuitsOver: the user quits (ctrl+c aborts, the second
// quits) while the model is still streaming. Run aborts the run and
// waits for its end before returning, so the caller can close the store.
func TestRunEndsARunItQuitsOver(t *testing.T) {
	r := start(t, scripted.New(scripted.SayThenBlock("part")))
	r.type_("go\r")
	r.waitScreen("part")
	r.type_("\x03")
	r.type_("\x03")
	if err := r.waitEnd(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.agent.State().Running {
		t.Error("Run returned with the run still going")
	}
}

// TestRunCopiesASelectionToTheClipboard: a drag sent as a terminal sends
// it in SGR mouse mode draws the selection, and ctrl+c copies it to the
// terminal's clipboard with OSC 52, written to the output the program
// renders to.
func TestRunCopiesASelectionToTheClipboard(t *testing.T) {
	r := start(t, scripted.New(scripted.Say("po", "ng")))
	r.type_("ping\r")
	r.waitScreen("pong")
	// The status line copied is the one at the copy, and the run that
	// printed pong is still ending: wait for it to be over before the
	// drag, or the copy races the relayout to idle.
	for r.agent.State().Running {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	// A drag over the status line, always the first line of the screen.
	r.type_("\x1b[<0;1;1M")   // press the left button at (0, 0)
	r.type_("\x1b[<32;80;1M") // drag to (79, 0)
	r.type_("\x1b[<0;80;1m")  // release: the selection stays, nothing copied
	r.type_("\x03")           // ctrl+c: the copy is a choice
	r.waitRaw("\x1b]52;c;")   // the clipboard was set
	out := r.out.Raw()
	i := strings.Index(out, "\x1b]52;c;")
	if i < 0 {
		t.Fatalf("no OSC 52 in the output:\n%q", out)
	}
	rest := out[i+len("\x1b]52;c;"):]
	end := strings.Index(rest, "\x07")
	if end < 0 {
		t.Fatalf("OSC 52 is not terminated:\n%q", out)
	}
	text, err := base64.StdEncoding.DecodeString(rest[:end])
	if err != nil {
		t.Fatalf("the clipboard text is not base64: %v", err)
	}
	if !strings.Contains(string(text), "model scripted") {
		t.Errorf("the status line was not copied: %q", text)
	}
	if strings.TrimRight(string(text), " ") != string(text) {
		t.Errorf("the status line's padding was copied: %q", text)
	}
}

// TestRunWarnsWhenARunWillNotEnd: a run that ignores its abort is
// reported through WithWarn after the drain timeout and Run still
// returns.
func TestRunWarnsWhenARunWillNotEnd(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	reached := make(chan struct{})
	stuck := func(_ context.Context, em *openresponses.Emitter) error {
		close(reached)
		<-release
		return context.Canceled
	}
	var mu sync.Mutex
	var warned []string
	r := start(t, scripted.New(stuck),
		console.WithDrainTimeout(100*time.Millisecond),
		console.WithWarn(func(s string) { mu.Lock(); warned = append(warned, s); mu.Unlock() }))
	r.type_("go\r")
	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatal("the model was never called")
	}
	r.type_("\x03")
	r.type_("\x03")
	r.waitEnd()
	mu.Lock()
	defer mu.Unlock()
	if len(warned) == 0 {
		t.Error("no warning for a run that did not end")
	}
}
