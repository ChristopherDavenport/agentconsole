# agentconsole

A terminal client for agents on the transparent-ai stack. The design of
record is `docs/plans/client.md`; read it before writing code. The rule
that shapes everything: the session record is the truth for what is
committed, and live events carry only what is not committed yet.

## Module

- Module path: `github.com/ChristopherDavenport/agentconsole`.
- Go 1.26 is the floor.
- The library surface is `client`, `client/native`, `client/kitbackend`,
  `view`, `toolview` and `console`; `tui` and `inspect` stay under
  `internal/`. An embedder (dax) imports the public ones and calls
  `console.Run`. Adding an exported name is a decision for the plan's
  "Decided in implementation", since embedders pin it.
  - `client`: the contract. `Backend`, `Control`, `Record`,
    `LiveEvent`. It knows the stack's types and no backend.
  - `client/native`: the in-process backend over an `agentturn.Agent`
    and the `session.Recorder` writing its store. It does not import
    agentkit.
  - `client/kitbackend`: the native backend over an `agentkit.Kit`: the
    agent built from the kit, the kit's recorder attached, the run
    context and grant scope the kit expects, `Answer` through the
    engine's `Release`, skill grants moved with `ContinueFrom`.
  - `view`: the reconciler. A pure model: record `Change`s and
    `LiveEvent`s in, what a client renders out. No goroutines, no I/O,
    no terminal.
  - `toolview`: per-tool renderers of calls, which an embedder hands
    `console.Run`. A renderer reads a `view.Call` (the record's facts
    about the call and its tool's schema), never the tool, and returns
    lines with roles; the TUI styles them.
  - `console`: `Run(ctx, backend, ...Option)`, the program, the feeds,
    signals and the drain at exit.
  - `internal/inspect`: the record-detail panes, computed on demand from
    a snapshot of the session (`Record.Read`), never from the follower's
    session. It decodes the records other products write (policy
    verdicts, skill grants, memory manifests) from their JSON.
  - `internal/tui`: the Bubble Tea model (conversation, turn status,
    permissions, input, the tree, the row cursor and the panes) and
    `Attach`, which feeds the view from the record and live streams and
    sends each `view.Model` to the program.
  - `internal/scripted`: the scripted model the tests share.
  - `internal/termtest`: a terminal emulator (`x/vt`) for the tests
    that run the program over a pipe, to read its screen.
  - `cmd/agentconsole`: the binary, a native agent in process, a thin
    user of `console.Run`.
- No ACP backend yet. A later step.

## Rules

- The record is the truth. The view never invents a committed fact from
  a live event: it shows an in-flight item as overlay until the entry
  carrying it lands, then drops the overlay copy, exactly once. An item
  is never shown twice and never dropped before its entry exists.
- The view waits for the entry, never for the event. The recorder lags
  its events in two places (a config entry is written by the next
  entry-writing event; a model item that completes before the stream
  names its response is written at `response_end`), so a live
  `item_end` does not mean the record holds the item.
- A run's end flushes what is left of the overlay, but only once the
  record has the run's end entry too: the live event can beat the
  followed entries, and flushing early would drop an item its entry is
  about to replace.
- A live event's item is the agent's accumulator and keeps changing.
  Copy it in the subscriber, before the callback returns.
- `Change.Session` is valid until the next step of the follow. The view
  copies what it keeps, never the session.
- The contract grows when a feature needs it, not before.

## The TUI

- The view is fed on goroutines under one lock (`tui.Attach`); the model
  only ever receives `ModelMsg` values and touches no view state.
- `Prompt` and `Answer` block: they run in tea.Cmds and end with a
  `runDoneMsg`.
- Tests drive `Update` and read `View()` against a real agent with a
  scripted model; no teatest.
- Anything that reads the session beyond the feed's steps (the tree's
  read-only views, the panes) runs in a `tea.Cmd` on a snapshot from
  `Record.Read`. Calling the feed from `Update` would deadlock: a step
  sends its model while holding the feed's lock, and the program's
  goroutine would be waiting for that lock.
- Viewing is local and continuing moves the head (`Control.ContinueFrom`,
  a rebase and a leaf label): see the plan.

## Everyday commands

```sh
make check      # fmt, tidy-check, vet, staticcheck, govulncheck, race tests
make release VERSION=vX.Y.Z   # see below; pushes
```

## Releases

Annotated `v*` tags, as in the siblings: `CHANGELOG.md` keeps an
`## Unreleased` section in Keep a Changelog form, and `make release
VERSION=vX.Y.Z` dates it, runs `make check`, commits, runs
`scripts/release-guard.sh` (clean tree, tag new locally and on origin,
version above everything published, the first release being any vX.Y.Z),
tags with the changelog section as the message and pushes the branch and
tag atomically. `make release-guard TAG=vX.Y.Z` runs the guard alone.
Never run `make release` from an agent session without being asked.

Every sibling is required at a released version. To try an unreleased
sibling, use a workspace file kept outside the repository
(`GOWORK=/path/to/agentconsole.work make check`); never commit a
`go.work` or a `replace`. Run make targets with `GOFLAGS=-mod=readonly`
where the Go environment sets `-mod=mod`.

## Tests

Headless, offline and under `-race`: a scripted model (an
`openresponses.Streamer` built on the emitter), a store from
`agentsession`, a real `agentturn.Agent` with a real recorder. Assert
the rendered model at each step, not the event stream. A test that runs
the program over a pipe reads the screen through `internal/termtest`,
never the bytes written: the renderer writes only the cells that change,
so a streamed word is seldom whole in them.
