# agentconsole

A terminal client for agents built on the transparent-ai stack
(`openresponses`, `agentturn`, `agentsession`), and for foreign agents
over ACP.

It renders a conversation from the agent's session record, and overlays
live events only for what the record does not hold yet. A conversation,
its branches, its policy decisions and its verification are one view,
whether the run is live or long finished.

Status: the conversation, turn, tree and record-detail views work against
an agent run in process. The ACP backend is a later step. The design is in
`docs/plans/client.md`.

## As a library

A coding agent embeds the client by building a backend over its own agent
and handing it to `console.Run`. For an agent assembled with agentkit:

```go
kit, err := agentkit.New(ctx,
	agentkit.WithModel(model, "gpt-5"),
	agentkit.WithTools(read, write, bash),
	agentkit.WithPolicy(policy, matchers),
	agentkit.WithSession(store, agentsession.Header{CWD: cwd}),
	// agentkit.WithResumedSession(store, id) to continue a session:
	// a call held for approval before the restart is answered after it.
)
if err != nil {
	return err
}
defer kit.Close()

be, err := kitbackend.New(kit)
if err != nil {
	return err
}
defer be.Close()
return console.Run(ctx, be) // returns when the user quits; the store is then safe to close
```

For an `agentturn.Agent` built by hand, attach its `session.Recorder`
and use `native.New(agent, rec)` instead; `native` does not import
agentkit. The public packages are `console`, `client`, `client/native`,
`client/kitbackend` and `view`; the terminal model and the record panes
are internal.

## Usage

```sh
go run ./cmd/agentconsole --model qwen3:1.7b
```

The model comes from an OpenAI-compatible Responses endpoint:
`--base-url` (default `http://localhost:11434/v1`, Ollama), `--model`,
and the API key from the environment variable named by `--api-key-env`
(default `OPENAI_API_KEY`). Sessions are recorded under `--store-root`
(`--store jsonl` or `cas`; default `~/.local/share/agentconsole/sessions`).

- `--session ID` resumes a session; `--session ref:NAME` resumes the one a
  ref points to.
- `--conversation NAME` continues the named conversation, creating it the
  first time.
- `--tools none|clock`, `--confirm-tools` (ask before every tool call),
  `--instructions`, `--max-turns`.

In the terminal: Enter sends a prompt, or steers while a run is going;
Ctrl-C (or SIGINT) aborts a run, and quits when idle; PgUp, PgDn, Ctrl-Up,
Ctrl-Down, Ctrl-Home, Ctrl-End and the mouse wheel scroll the conversation,
while the prompt wraps over up to five lines, then scrolls, and the
arrows, Home and End move the cursor within it; Ctrl-R shows reasoning; Ctrl-O shows tool
arguments and output in full. When a tool call needs permission, y
approves and n refuses, with an optional reason. Ctrl-/ (or F1) lists every
key; Esc, q or Ctrl-/ again goes back.

The tree and the record detail:

- `ctrl+t` switches between the conversation and the tree of the session's
  branches, its origin (if it is a fork) and the sessions it linked. In the
  tree, up/down select, Enter views a branch or opens the origin read-only
  (Esc returns to the live session; the agent's head does not move), and
  `c` continues from the selected branch: the agent's head moves there and
  the next prompt continues from it.
- `ctrl+p` and `ctrl+n` move a cursor over the conversation's rows, and
  `ctrl+b` continues from the selected row. Esc clears the cursor. A left
  click selects the row under it; a click on the row already selected
  expands or collapses it. With a row selected, Ctrl-R and Ctrl-O show or
  hide that row's reasoning or tool output alone; with none, every row's.
- `tab` opens the detail of the selected row (its entry, whether its
  request verifies and why not, the response's model and usage, a call's
  policy decision, who decided, dispatch, output and skill grants, what a
  compaction folded), then the session summary (header, verification over
  the line, token usage by model, config, refs, memory manifest, and the
  cost when a host supplies a price source), then closes the pane.
