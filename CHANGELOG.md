# Changelog

All user-visible changes to this library and binary. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
uses [Semantic Versioning](https://semver.org/); before v1.0.0 minor
versions may break the API.

## v0.0.7 - 2026-10-06

- Added: the assistant's messages are rendered as markdown with glamour
  (CommonMark with GitHub's tables, task lists, strikethrough and bare
  links, code blocks highlighted), in its dark or light style by the
  terminal's background, wrapped to the conversation's width. Its style
  is fitted to the conversation: no margin, no "##" before headings,
  inline code a muted gray, a one-column quote bar. Links are OSC 8
  hyperlinks. What the user typed is shown as typed. New dependency:
  `charm.land/glamour/v2`; `github.com/yuin/goldmark`,
  `golang.org/x/text` and `golang.org/x/net` are required past the
  advisories in the versions glamour asks for.
- Changed: the run's state (running, aborting, requires action, idle)
  moves from the status line at the top to a run line of its own above
  the input, in color, with a spinner at its right edge while
  a run goes. A tool call's row starts with a dot as the run line does,
  in place of its gear: ● in motion (its arguments streaming, or the tool
  running), ◆ waiting on a permission, ○ at rest, so its name lines up
  with the run's state. A call in motion is drawn in the run line's color,
  with the same spinner at the right edge, in the run line's spinner's
  column. A blank line sits on each side of the run line.
- Added: the run line shows the current turn's figures after its state,
  while the run goes or waits on a permission: how long it has run and
  the tokens its model calls took in and gave out, as
  "(19s · 1.3M↑ / 534k↓)". `view.Model.Run`, of the new type `view.Run`,
  carries them: the last run on the viewed line, by its start and end
  entries, and the usage of its model calls. Token counts keep three
  figures (534k, not 534.0k), on the status line too.
- Added: the status line leads with the session's time working, as
  "time 4m12s": the runs on the line, each from its start entry to its
  end entry, and the current turn's time so far. `view.Model.Worked`
  carries the runs that ended.
- Fixed: the input is drawn on the terminal's own background. bubbles'
  textarea gave the line the cursor is on a background of its own (ANSI
  black on a dark terminal, color 255 on a light one), a band of another
  color across the input on most terminals.
- Changed: the terminal toolkit is Bubble Tea v2 (`charm.land/bubbletea/v2`
  v2.0.10, with bubbles v2 and lipgloss v2), and Go 1.26 is the floor. No
  exported name changed. Ctrl-/ opens the keys on terminals that speak the
  kitty keyboard protocol, which name it as itself. A paste never answers
  a permission or a question, and goes nowhere on the tree, the keys or a
  read-only view.
- Added: the MIT license.
- Fixed: a mouse selection on the conversation stays on the text it
  covers. Before, it was fixed to the screen, so a run streaming below it
  (or the wheel) moved the text out from under it and ctrl+c copied
  whatever had scrolled into its place. Now it moves with the text, and
  ctrl+c copies the selected text even when it has scrolled out of sight.
  A selection on the status line, panes or input stays fixed to the
  screen.
- Added: a click on the input puts its cursor on the cell clicked (the
  end of the row when past its text), and leaves the selected row and
  its pane, as Esc does.
- Added: a click on an item of the tree selects it, and a click on the
  item already selected opens it, as Enter does.

## v0.0.6 - 2026-10-04

- Changed: a function call's row is one line while it is collapsed. The
  arguments show as their key=value pairs, each value clipped to 60
  runes, instead of the raw JSON, and a call that has ended shows none
  of its output, only a hint of how many lines it holds; Ctrl-O or a
  second click on the row shows the raw arguments and the whole output
  as before. The state suffix stays only while the call has not ended,
  and a running call shows the last lines of the output a tool reports
  as it goes, five by default, instead of only the first.
- Fixed: the session pane did not refresh when a run ended: the final
  update of the run's view could arrive while the prompt was still
  returning, with the pane still held for the run, and nothing after
  that asked for its data again. It is recomputed when the prompt
  returns.

## v0.0.5 - 2026-10-04

- Added: a mouse drag selects text on any part of the terminal client's
  screen — the conversation, the panes, the top bar — and the selection
  stays drawn until the next key or press; the terminal's own selection
  cannot reach the client, since the client has the mouse. Ctrl-C with
  a selection drawn copies it to the terminal's clipboard with OSC 52,
  which works over ssh too, as a desktop's copy does; it drops the
  selection, so the next Ctrl-C is the abort or the quit it always was,
  and with nothing selected Ctrl-C is that interrupt. A click with no
  drag between the press and the release still selects the row under it
  (a second one expands it). The copied text is what the selection holds
  with the layout's padding trimmed; a terminal that does not take
  OSC 52 (iTerm2 needs "Applications in terminal may access clipboard"
  on) copies nothing. `tui.WithCopier` and `tui.Clipboard`, where a copy
  goes, are the console's: `console.Run` writes the sequence to the
  output the program renders to.

## v0.0.4 - 2026-10-04

- Added: `client.Cost` names the price hook, and `console.WithCost` /
  `tui.WithCost` feed it to the client. The status line shows a running
  total of token usage and the session's cost, and the session pane shows
  usage split by model; a model without a price is named as unpriced.
- Changed: the prompt is a wrapping text area that grows to five rows,
  then scrolls, instead of one line that scrolled sideways, and a pasted
  value keeps its newlines.

## v0.0.3 - 2026-10-04

- Added: questions a running call puts to the user. A host asks with
  `native.Backend.Ask` (and `kitbackend.Backend`, by embedding), which
  blocks until the client replies or the call's context ends; live
  subscribers get `client.QuestionAsked` and `client.QuestionClosed`, one
  that attaches while a question waits included, and `Control.Reply`
  answers. The view lists them as `Model.Questions`.
- Added: the terminal client asks a question while the run goes, ahead
  of a permission: `y` allows, `n` refuses with an optional reason, `Esc`
  goes back. A sub-agent's call its policy holds and a tool's own yes/no
  question are both asked this way.
- Changed: `client.Control` has `Reply`; a backend of an embedder's own
  implements it.

## v0.0.2 - 2026-10-03

- Added: with a row selected, Ctrl-O and Ctrl-R expand or collapse that
  row alone; with none they still apply to every row, and doing so drops
  the rows' own toggles.
- Added: a left click selects the row under it, as Ctrl-P and Ctrl-N do;
  a click on the row already selected expands or collapses it.
- Changed: a bar above and below the input line marks where you type.
- Added: Ctrl-/ (or F1) shows every key the client takes; the input's
  placeholder says so.

## v0.0.1 - 2026-10-03

First release. agentconsole is a terminal client for agents on the
transparent-ai stack, and a library a coding agent embeds to get that
client. The design of record is `docs/plans/client.md`. The rule that
shapes it: the session record is the truth for what is committed, and
live events carry only what is not committed yet.

- Added: the contract, package `client`. `Backend` gives a client
  `Control` to act on the agent (`Prompt`, `Steer`, `Abort`, `Answer`,
  `State`, `ContinueFrom`), `Live` for what is not committed yet, and
  `Record` for what is (`Follow`, `Read`, `Refs`, `Verified`). It speaks
  the stack's own types, `openresponses.Item`, `agentturn.Answer` and
  `agentsession.Change`. `LiveEvent` narrows `agentturn.Event` to the
  events that carry no committed fact.
- Added: the reconciler, package `view`. A pure model that takes record
  changes and live events in and gives what a client renders out: the
  conversation's rows, with an in-flight item shown as overlay until the
  entry carrying it lands and then dropped, once; the turn's state; the
  permissions to answer; the calls a run left cut off; the branches. It
  waits for the entry and never for the event, and a run's end flushes
  the overlay only once the record has the run's end. A property test
  plays scripted runs against random interleavings of the two streams,
  resets and head moves.
- Added: the in-process backend, package `client/native`, over an
  `agentturn.Agent` and the `session.Recorder` writing its store. `Live`
  is the agent's subscription with an unbounded queue, so a slow client
  never stalls a run; `Record` follows the store with no polling.
  `ContinueFrom` moves the agent's head: it rebases the recorder, seeds
  the agent with the transcript and the pending calls of the branch,
  carries the reasoning attribution of the models, records the move as a
  leaf label, and puts everything back if any step fails. A leaf label, a
  fork's prefix and an entry that leaves a call without an output are
  refused.
- Added: the terminal client, a Bubble Tea model. The conversation, with
  streaming messages, reasoning (ctrl+r) and tool arguments and output
  (ctrl+o); the turn's state; permissions answered in place (y approves,
  n refuses with an optional reason); Enter prompts, or steers while a run
  goes; ctrl+c aborts a run and quits when idle; scrolling by keys and the
  mouse wheel.
- Added: the tree (ctrl+t). The session's branches, its origin when it is
  a fork and the sessions it linked. Up and down select, Enter views a
  branch or opens the origin read-only, and `c` continues from the
  selected branch, which moves the agent's head. A cursor over the
  conversation's rows (ctrl+p, ctrl+n) continues from a row with ctrl+b.
- Added: the record detail (tab). The selected row's entry, whether its
  request verifies and why not, the response's model and usage, a call's
  policy decision and who decided it, its dispatch, output and the skill
  grants in force, what a compaction folded; then the session summary
  (header, verification over the line, config, refs, the memory manifest
  with its checks and refusals). It is computed on demand from a snapshot
  of the session, never from the follower's.
- Added: package `console`, `Run(ctx, backend, ...Option) error`. It runs
  the program over any backend and does what the binary does: attaches
  the feeds before the program starts so no event of the first run is
  missed, turns SIGINT and SIGTERM into the client's interrupt, and after
  the program ends aborts a run still going and waits for it to write its
  end, so the caller can close its store. Options: `WithInput`,
  `WithOutput`, `WithWindowSize`, `WithDrainTimeout`, `WithWarn`,
  `WithoutSignalHandler`.
- Added: package `client/kitbackend`, `New(kit *agentkit.Kit, ...Option)`.
  The native backend over an `agentkit.Kit`: it builds the agent from the
  kit's config and options, attaches the kit's recorder and follows its
  store. Every run carries the kit's recorder and session, so the verdicts,
  the memory manifest, folds, a tool's question and its answer, and the
  skill grants of the conversation land in the session the client shows.
  `Answer` releases the calls the kit's engine held through
  `Engine.Release`; `ContinueFrom` ends the conversation's skill grants
  before the head leaves a branch and grants again those of the new one. A
  kit built with `agentkit.WithResumedSession` restores its pending calls,
  so a call held for approval before a restart is answered after it.
- Added: `native.New` takes options for a host that needs more than the
  agent: `WithRunContext`, `WithRelease` and `WithHeadMove`.
- Added: the binary, `cmd/agentconsole`, an agent in process against an
  OpenAI-compatible Responses endpoint (Ollama by default), recorded in a
  jsonl or content-addressed store, resumable by session ID or by ref, or
  by a named conversation.
- Changed: `client`, `client/native` and `view` are public packages and
  the stable surface of the library; the terminal model (`internal/tui`)
  and the record panes (`internal/inspect`) stay internal.
- Fixed: the detail pane recognises a skill's grants when the product
  names the rule source itself (`skill:NAME`) and not only agentkit's
  default (`agentskill:NAME`).
- Dependencies: agentkit v0.0.7, agentsession v0.0.21, agentturn and
  agentturn/session v0.0.16, agenttool v0.0.15, openresponses v0.0.14.
