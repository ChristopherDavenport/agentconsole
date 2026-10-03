# agentconsole

A terminal client for agents built on the transparent-ai stack
(`openresponses`, `agentturn`, `agentsession`), and for foreign agents
over ACP.

It renders a conversation from the agent's session record, and overlays
live events only for what the record does not hold yet. A conversation,
its branches, its policy decisions and its verification are one view,
whether the run is live or long finished.

Status: the conversation and turn views work against an agent run in
process. The tree and record-detail views and the ACP backend are later
steps. The design is in `docs/plans/client.md`.

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
