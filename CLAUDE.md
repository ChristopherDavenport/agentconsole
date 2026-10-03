# agentconsole

A terminal client for agents on the transparent-ai stack. The design of
record is `docs/plans/client.md`; read it before writing code. The rule
that shapes everything: the session record is the truth for what is
committed, and live events carry only what is not committed yet.

## Module

- Module path: `github.com/ChristopherDavenport/agentconsole`.
- Go 1.25 is the floor.
- Everything is under `internal/` until a second consumer needs the
  contract (plan, order of work step 5).
  - `internal/client`: the contract. `Backend`, `Control`, `Record`,
    `LiveEvent`. It knows the stack's types and no backend.
  - `internal/client/native`: the in-process backend over an
    `agentturn.Agent` and the `session.Recorder` writing its store.
  - `internal/view`: the reconciler. A pure model: record `Change`s and
    `LiveEvent`s in, what a client renders out. No goroutines, no I/O,
    no terminal.
- No TUI yet; no ACP backend yet. Both are later steps of the plan.

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

## Everyday commands

```sh
make check      # fmt, tidy-check, vet, staticcheck, govulncheck, race tests
```

Every sibling is required at a released version. To try an unreleased
sibling, use a workspace file kept outside the repository
(`GOWORK=/path/to/agentconsole.work make check`); never commit a
`go.work` or a `replace`. Run make targets with `GOFLAGS=-mod=readonly`
where the Go environment sets `-mod=mod`.

## Tests

Headless, offline and under `-race`: a scripted model (an
`openresponses.Streamer` built on the emitter), a store from
`agentsession`, a real `agentturn.Agent` with a real recorder. Assert
the rendered model at each step, not the event stream.
