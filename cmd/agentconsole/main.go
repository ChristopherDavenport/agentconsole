// Command agentconsole is a terminal client that runs an agent in
// process: a model behind an OpenAI-compatible Responses endpoint
// (Ollama's /v1 by default), with its session recorded in a store the
// client follows.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/internal/client/native"
	"github.com/ChristopherDavenport/agentconsole/internal/tui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "agentconsole:", err)
		os.Exit(1)
	}
}

// drainTimeout bounds the wait for an aborted run to write its end.
const drainTimeout = 3 * time.Second

type clockArgs struct{}

func run() error {
	var (
		baseURL      = flag.String("base-url", "http://localhost:11434/v1", "base URL of an OpenAI-compatible Responses endpoint")
		model        = flag.String("model", "", "model name (required)")
		apiKeyEnv    = flag.String("api-key-env", "OPENAI_API_KEY", "environment variable holding the API key, if the endpoint needs one")
		storeKind    = flag.String("store", "jsonl", "session store: jsonl or cas")
		storeRoot    = flag.String("store-root", defaultStoreRoot(), "directory of the session store")
		resume       = flag.String("session", "", "resume a session by ID, or by ref with ref:NAME")
		conversation = flag.String("conversation", "", "continue the session the ref NAME points to, creating it when there is none")
		instructions = flag.String("instructions", "", "instructions for the model")
		tools        = flag.String("tools", "clock", "tools to give the agent: none or clock")
		confirm      = flag.Bool("confirm-tools", false, "ask before running any tool call")
		maxTurns     = flag.Int("max-turns", 20, "most model calls in one run")
	)
	flag.Parse()
	if *model == "" {
		return fmt.Errorf("--model is required (for Ollama, a name from `ollama list`)")
	}

	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	st, closeStore, err := openStore(*storeKind, *storeRoot)
	if err != nil {
		return err
	}
	defer closeStore()

	rec, seed, err := openSession(ctx, st, *resume, *conversation, session.WithHarness("agentconsole", "dev"))
	if err != nil {
		return err
	}

	var clientOpts []openresponses.ClientOption
	if key := os.Getenv(*apiKeyEnv); key != "" {
		clientOpts = append(clientOpts, openresponses.WithAPIKey(key))
	}
	cfg := agentturn.Config{
		Model:        openresponses.NewClient(*baseURL, clientOpts...).AsAdapter(),
		ModelName:    *model,
		Instructions: *instructions,
		MaxTurns:     *maxTurns,
	}
	switch *tools {
	case "none":
	case "clock":
		cfg.Tools = []agenttool.Tool{agenttool.New("clock", "the current date and time", func(context.Context, clockArgs) (string, error) {
			return time.Now().Format(time.RFC1123), nil
		})}
	default:
		return fmt.Errorf("unknown --tools %q, want none or clock", *tools)
	}
	if *confirm {
		cfg.BeforeToolCall = func(_ context.Context, info agentturn.ToolCallInfo) (*agentturn.ToolDecision, error) {
			return &agentturn.ToolDecision{Action: agentturn.Defer, Reason: "Run " + info.Call.Name + "?"}, nil
		}
	}

	ag := agentturn.New(cfg, seed...)
	detach := rec.Attach(ag)
	defer detach()
	be, err := native.New(ag, rec)
	if err != nil {
		return err
	}

	m := tui.New(ctx, be)
	// In raw mode ctrl+c is a key. A signal from outside (kill, a parent's
	// ctrl+c) is made the same thing: the program's own handling would
	// end it with an error, without aborting the run.
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(ctx), tea.WithoutSignalHandler())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	go func() {
		for {
			select {
			case <-sigs:
				p.Send(tui.InterruptMsg{})
			case <-ctx.Done():
				return
			}
		}
	}()
	wait := tui.Attach(ctx, be, p.Send)
	_, err = p.Run()
	// A run may still be going (a second ctrl+c quits without waiting for
	// an abort to land). Let it write its end before the store closes;
	// the context's cancel is the last resort.
	be.Control().Abort()
	if !m.Drain(drainTimeout) {
		fmt.Fprintln(os.Stderr, "agentconsole: a run did not end; closing the session as it is")
	}
	stop()
	m.Drain(drainTimeout)
	wait()
	if err == nil {
		fmt.Fprintln(os.Stderr, "session", be.SessionID())
	}
	return err
}
