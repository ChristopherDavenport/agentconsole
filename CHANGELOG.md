# Changelog

All user-visible changes to this library and binary. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
uses [Semantic Versioning](https://semver.org/); before v1.0.0 minor
versions may break the API.

## Unreleased

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
