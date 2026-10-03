package console_test

import (
	"bytes"
	"context"
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
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// rig is an agent, its recorder and a backend over a store, and a
// console.Run on a pipe.
type rig struct {
	t     *testing.T
	agent *agentturn.Agent
	be    *native.Backend
	in    *io.PipeWriter
	out   *syncBuf
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
	r := &rig{t: t, agent: ag, be: be, in: pw, out: &syncBuf{}, done: make(chan error, 1)}
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

func (r *rig) waitOutput(sub string) {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(r.out.String(), sub) {
		if time.Now().After(deadline) {
			r.t.Fatalf("%q never shown; output:\n%q", sub, r.out.String())
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
	r.waitOutput("pong")
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
	r.waitOutput("part")
	r.type_("\x03")
	r.type_("\x03")
	if err := r.waitEnd(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.agent.State().Running {
		t.Error("Run returned with the run still going")
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
