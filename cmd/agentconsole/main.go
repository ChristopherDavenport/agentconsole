// Command agentconsole is a terminal client that runs an agent in
// process: a model behind an OpenAI-compatible Responses endpoint
// (Ollama's /v1 by default), assembled with agentkit and driven through
// its Kit.Control, with its session recorded in a store the client
// follows. It is the example of an embedder; the library packages do
// not import agentkit.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ChristopherDavenport/agentkit"
	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agenttool"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client"
	"github.com/ChristopherDavenport/agentconsole/console"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "agentconsole:", err)
		os.Exit(1)
	}
}

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

	ctx := context.Background()

	st, closeStore, err := openStore(*storeKind, *storeRoot)
	if err != nil {
		return err
	}
	defer closeStore()

	rec, seed, err := openSession(ctx, st, *resume, *conversation, session.WithHarness(harnessName, harnessVersion))
	if err != nil {
		return err
	}

	var clientOpts []openresponses.ClientOption
	if key := os.Getenv(*apiKeyEnv); key != "" {
		clientOpts = append(clientOpts, openresponses.WithAPIKey(key))
	}
	kopts := []agentkit.Option{
		// The endpoint is a provider's own API, which accepts no other
		// provider's slug-prefixed items, so the adapter leaves them out
		// (openresponses docs/extensions.md 1.6).
		agentkit.WithModel(openresponses.NewClient(*baseURL, clientOpts...).AsAdapter(openresponses.WithProviderSlugs()), *model),
		agentkit.WithMaxTurns(*maxTurns),
		agentkit.WithRecorder(rec),
		// The client answers the agent's questions, so they are events.
		agentkit.WithQuestionEvents(),
	}
	if *instructions != "" {
		kopts = append(kopts, agentkit.WithInstructions(*instructions))
	}
	switch *tools {
	case "none":
	case "clock":
		kopts = append(kopts, agentkit.WithTools(agenttool.New("clock", "the current date and time", func(context.Context, clockArgs) (string, error) {
			return time.Now().Format(time.RFC1123), nil
		})))
	default:
		return fmt.Errorf("unknown --tools %q, want none or clock", *tools)
	}
	if *confirm {
		kopts = append(kopts, agentkit.WithBeforeToolCall(func(_ context.Context, info agentturn.ToolCallInfo) (*agentturn.ToolDecision, error) {
			return &agentturn.ToolDecision{Action: agentturn.Defer, Reason: "Run " + info.Call.Name + "?"}, nil
		}))
	}
	kit, err := agentkit.New(ctx, kopts...)
	if err != nil {
		return err
	}
	defer kit.Close()

	// The kit records into rec, which the client attaches: the recorder
	// subscribes ahead of the client, so an event's entry is in the store
	// before the client sees the event.
	ag := agentturn.New(kit.Config(), append(kit.AgentOptions(), seed...)...)
	detach := rec.Attach(ag)
	defer detach()
	ctl, err := kit.Control(ag)
	if err != nil {
		return err
	}
	record, err := client.RecordOf(rec)
	if err != nil {
		return err
	}

	err = console.Run(ctx, record, ctl, typedAt(client.Direct(ctl), atTerminal))
	if err == nil {
		fmt.Fprintln(os.Stderr, "session", rec.SessionID())
	}
	return err
}

// atTerminal is who sends what this binary's terminal sends: the person
// at the keys, so the record holds what they type as their words
// (agentsession format 0.21's trigger kind human).
var atTerminal = agentturn.Trigger{Kind: agentsession.TriggerHuman, Source: "terminal"}

// typedAt is c with t claimed for what the person sends: the trigger of
// a Prompt's and a Queue's input, and the answerer of a Reply. The view
// sends intents and holds no identity; the embedder knows who is at the
// keys and says so. This binary takes every line as typed, having no
// commands of its own, and a message it composed from layers would be
// recorded without the kind human whatever its trigger.
func typedAt(c client.Controller, t agentturn.Trigger) client.Controller {
	return func(ctx context.Context, in client.Intent) error {
		switch in.(type) {
		case client.Prompt, client.Queue:
			ctx = agentturn.ContextWithTrigger(ctx, t)
		case client.Reply:
			ctx = agentturn.ContextWithAnswerer(ctx, t)
		}
		return c(ctx, in)
	}
}
