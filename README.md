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

The client is a view, not a controller. It reads the session through a
`client.Record` and the agent's events through a `client.Source`, and
sends what the user asks (a prompt, a steer, an answer, a reply, an
abort, a head move) as intents to a `client.Controller`, which decides
what they do. A coding agent embeds it by handing `console.Run` the
three. For an agent driven through agentturn's control contract, the
control is the source and `client.Direct` maps intents onto it; with
agentkit, the kit's `Control` does what a front needs around each run
(the recorder on the run's context, the engine's release of held calls,
the head move with the skill grants):

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

agent := agentturn.New(kit.Config(), kit.AgentOptions()...)
defer kit.Attach(agent)() // the kit's recorder, ahead of the client
ctl, err := kit.Control(agent)
if err != nil {
	return err
}
rec, err := client.RecordOf(kit.Recorder())
if err != nil {
	return err
}
return console.Run(ctx, rec, ctl, client.Direct(ctl)) // returns when the user quits; the store is then safe to close
```

Any `agentturn.Control` will do: a bare `*agentturn.Agent` with its
`session.Recorder` attached, or a remote agent through agentturn's
`front/control` (`control.Dial`), whose session a store client reads.
`Direct` moves the head when the control has a `ContinueFrom` (agentkit's
`Control` has) and refuses it otherwise, which the tree shows. A product
with intents of its own to handle (answering some questions by rule,
its own commands, recording who answered) writes its own `Controller`
and may pass the rest to `Direct`. `Direct` steers through the
control's `Accept` when it has one, and claims nothing for the person:
an embedder that knows a person typed a line puts a trigger of kind
`human` on the context of the `Prompt` and `Queue` intents it passes
on (`agentturn.ContextWithTrigger`), so the session records the line as
their words, as this module's binary does. The public packages are `console`,
`client`, `view` and `toolview`; the terminal model and the record panes
are internal, and none of them imports agentkit.

A call to a tool shows its name and its arguments as `key=value` pairs.
To draw a tool's calls its own way (a command as a shell line, an edit
as a diff), give the client a `toolview.Renderer` for it:

```go
console.Run(ctx, rec, ctl, client.Direct(ctl), console.WithToolRenderers(toolview.Renderers{"bash": bashRenderer{}}))
```

A renderer reads what the session records of a call (the arguments, the
state, the output, the tool's schema), never the tool, so it draws a
live call, a historical one and an MCP tool's alike, and a call it
declines is shown the default way.

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

In the terminal: Enter sends a prompt, or steers while a run is going (the
steer is listed as queued over the input until the run takes it);
Shift-Enter starts a new line in the prompt (Alt-Enter and Ctrl-J do too,
for a terminal that sends Shift-Enter as a plain Enter);
Ctrl-C (or SIGINT) aborts a run, and quits when idle; PgUp, PgDn, Ctrl-Up,
Ctrl-Down, Ctrl-Home, Ctrl-End and the mouse wheel scroll the conversation,
while the prompt wraps over up to five lines, then scrolls, and the
arrows, Home and End move the cursor within it; Up on its first line
and Down on its last walk the lines sent before when the host keeps a
history (`console.WithHistory`; this binary keeps none, dax does);
Ctrl-G opens the prompt in `$VISUAL` or `$EDITOR` (one that waits for
the file, `code --wait`) and puts what is saved back in the input,
unsent; Ctrl-R shows reasoning; Ctrl-O shows tool
arguments and output in full. A mouse drag selects text anywhere on the
screen — the client has the mouse, so the terminal's own selection
cannot reach it — and the selection stays drawn until the next key or
press; Ctrl-C copies it to the terminal's clipboard with OSC 52, which
works over ssh too (a terminal that does not take it, iTerm2 with
"Applications in terminal may access clipboard" off, copies nothing).
With a selection drawn the first Ctrl-C is the copy and the second the
abort or the quit; with nothing selected, Ctrl-C is the interrupt it
always was.
The status line keeps the session's time working (its
runs' time, summed), a running total of tokens used and, when the host supplies a price source, what the session
has cost. A host makes the line up with `console.WithStatus`: on top or
at the bottom (under the input, its notices on a row just over it), plain or powerline, the segments it
names in its order (time, tokens, cost, model, context, retry, turn,
session, verified, stopped, and segments of its own it updates), in
colors that can change with a number, as the context does when it
fills against the window the host gives. A line too wide for the
terminal drops whole segments from the right. The model segment names the
model the host says it runs next (this binary's `-model`), and while the
session's record still names the last run's, that one beside it, "model
B (record: A until the next run)". Shift-Tab asks the host for its
next mode (a host segment shows the mode); a host without modes, this
binary among them, refuses, and the refusal shows on the status line. The line above the input is the current turn's: how long it
has run and the tokens it took in and gave out, and while a run goes,
what it is doing (waiting on the model, thinking, writing, calling or
running a tool, retrying), in color, its dot a spinner; idle, or waiting
on a permission, it says so. A tool call in motion carries the same
spinner in its dot and the same color, and its dot goes back to ○ when
it ends. A message in the conversation is labelled by whose words
the session records it as: "user" for what the person typed (a
trigger of kind `human`), where it came from and "not your words" for
a message the host composed, and "user (unclaimed)" for one the record
makes no claim about, as every message of a session recorded before
format 0.21 is. When a tool call needs permission, y
approves it; n refuses it and lets the run go on, the model reading the
refusal, and s refuses it and stops the run, each with an optional
reason. A host that offers a rule for the call (`console.WithGrantOffer`)
adds g, approve and allow the rule for the session, and G, the same
kept beyond it; the grant is the host's to make. A host that completes
the input (`console.WithCompletion`: its slash commands, its workspace's
paths for an `@path`) lists what it offers for the word being typed
over the input; Up and Down choose, Tab puts it in, Esc closes the list,
and with no list Tab cycles the pane. An `@path` is sent as typed, for
the host to read; the client reads no file. An input the host queued
hidden, and a hidden item, are left out unless the host lists them
(`console.WithHidden`), marked hidden and never as the person's words.
Ctrl-/ (or F1) lists every
key; Esc, q or Ctrl-/ again goes back.

The tree and the record detail:

- `ctrl+t` switches between the conversation and the tree of the session's
  branches, its origin (if it is a fork), the sessions it linked and,
  when the record can list them, the sessions made from it (its forks,
  with the entry each was made at). A branch whose line left calls
  waiting for an answer, or cut off by a process that stopped, says how
  many ("1 call waiting"); moving the head there shows them. In the
  tree, up/down select, Enter views a branch or opens the origin read-only
  (Esc returns to the live session; the agent's head does not move), and
  `c` continues from the selected branch: the agent's head moves there and
  the next prompt continues from it. A click selects an item of the tree,
  and a click on the item already selected opens it, as Enter does.
- `ctrl+p` and `ctrl+n` move a cursor over the conversation's rows, and
  `ctrl+b` continues from the selected row. Esc clears the cursor. A left
  click selects the row under it; a click on the row already selected
  expands or collapses it. A click on the input clears the cursor, as Esc
  does, and puts the input's cursor where the click was. With a row selected, Ctrl-R and Ctrl-O show or
  hide that row's reasoning or tool output alone; with none, every row's.
- `tab` opens the detail of the selected row (its entry, whether its
  request verifies and why not, the response's model and usage, a call's
  policy decision, who decided, dispatch, output, skill grants and the
  policy in force when the host had replaced it, what a compaction
  folded), then the session summary (header, verification over the line,
  token usage by model, config, refs, memory manifest, skill grants and
  what ended them, each replacement of the policy with its new default
  and reason, and the cost when a host supplies a price source), then
  closes the pane.

## License

MIT. See `LICENSE`.
