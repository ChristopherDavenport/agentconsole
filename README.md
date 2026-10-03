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
while the arrows, Home and End edit the input line; Ctrl-R shows reasoning; Ctrl-O shows tool
arguments and output in full. When a tool call needs permission, y
approves and n refuses, with an optional reason.

The tree and the record detail:

- `ctrl+t` switches between the conversation and the tree of the session's
  branches, its origin (if it is a fork) and the sessions it linked. In the
  tree, up/down select, Enter views a branch or opens the origin read-only
  (Esc returns to the live session; the agent's head does not move), and
  `c` continues from the selected branch: the agent's head moves there and
  the next prompt continues from it.
- `ctrl+p` and `ctrl+n` move a cursor over the conversation's rows, and
  `ctrl+b` continues from the selected row. Esc clears the cursor.
- `tab` opens the detail of the selected row (its entry, whether its
  request verifies and why not, the response's model and usage, a call's
  policy decision, who decided, dispatch, output and skill grants, what a
  compaction folded), then the session summary (header, verification over
  the line, config, refs, memory manifest), then closes the pane.
