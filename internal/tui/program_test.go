package tui_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentsession/jsonl"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"

	"github.com/ChristopherDavenport/agentconsole/internal/client/native"
	"github.com/ChristopherDavenport/agentconsole/internal/tui"
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

// TestRunsUnderARealProgram drives the model with a tea.Program over a
// pipe: bytes in as a terminal would send them, a reply streamed through
// Attach and Send, and ctrl+c, with the agent idle, ending the program.
func TestRunsUnderARealProgram(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := jsonl.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rec, _, err := session.Start(ctx, store, agentsession.Header{Records: agentsession.AllRecords})
	if err != nil {
		t.Fatal(err)
	}
	ag := agentturn.New(cfgWith(say(nil, nil, "pong")))
	defer rec.Attach(ag)()
	be, err := native.New(ag, rec)
	if err != nil {
		t.Fatal(err)
	}

	pr, pw := io.Pipe()
	defer pw.Close()
	out := &syncBuf{}
	p := tea.NewProgram(tui.New(ctx, be), tea.WithInput(pr), tea.WithOutput(out), tea.WithContext(ctx))
	wait := tui.Attach(ctx, be, p.Send)
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	p.Send(tea.WindowSizeMsg{Width: 80, Height: 20})

	pw.Write([]byte("ping\r"))
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out.String(), "pong") {
		if time.Now().After(deadline) {
			t.Fatalf("no reply on the terminal; output:\n%q", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Let the run settle to idle before quitting.
	for ag.State().Running {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	pw.Write([]byte{0x03})
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("program: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ctrl+c did not end the program")
	}
	cancel()
	wait()
}
