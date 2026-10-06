package tui_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentsession/jsonl"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"

	"github.com/ChristopherDavenport/agentconsole/client/native"
	"github.com/ChristopherDavenport/agentconsole/internal/termtest"
	"github.com/ChristopherDavenport/agentconsole/internal/tui"
)

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
	ag := agentturn.New(cfgWith(say(nil, nil, "**pong** [docs](https://example.com/docs)")))
	defer rec.Attach(ag)()
	be, err := native.New(ag, rec)
	if err != nil {
		t.Fatal(err)
	}

	pr, pw := io.Pipe()
	defer pw.Close()
	out := termtest.New(80, 20)
	defer out.Close()
	p := tea.NewProgram(tui.New(ctx, be), tea.WithInput(pr), tea.WithOutput(out), tea.WithContext(ctx), tea.WithWindowSize(80, 20), tea.WithColorProfile(colorprofile.ANSI))
	wait := tui.Attach(ctx, be, p.Send)
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()

	pw.Write([]byte("ping\r"))
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out.Screen(), "pong docs") {
		if time.Now().After(deadline) {
			t.Fatalf("no reply on the terminal; screen:\n%s", out.Screen())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The reply's link reaches the terminal as a hyperlink. (A program
	// whose output is not a terminal writes no styles, hyperlinks among
	// them, unless it is given a profile.)
	if !strings.Contains(out.Raw(), ";https://example.com/docs\a") {
		t.Errorf("the reply's link is not a hyperlink: written %q", out.Raw())
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
