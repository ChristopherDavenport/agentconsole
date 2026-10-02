# Plan: the client contract

agentconsole is a terminal client for agents. It has two aims. The
first is modularity: the same client drives an agentturn agent in
process, one behind a wire, or a foreign agent over ACP. The second is
transparency: what the client shows is what the session records, so a
conversation, its branches, its policy decisions and its verification
are one view, live or months later. Written 2026-10-02, after the
release wave that shipped agentsession format 0.11 and agentturn
v0.0.16.

This plan settles what the client depends on. The TUI is the first
consumer, and the contract stays an internal package until a second
consumer needs it.

## The two projections

The stack already defines both halves of a running agent, over one
vocabulary.

| layer | defined by | what it is |
|---|---|---|
| vocabulary | openresponses `Item` | messages, reasoning, function calls and outputs: shared by the model wire, the loop and the record |
| turn | `agentturn.Agent` | **control**: `Prompt`, `Steer`, `Deliver`, `Resume(answers)`, `Abort`, `SetConfig`, `SetTranscript`, `State`. **live**: `Subscribe(Event)` — item and tool lifecycle, turn and run boundaries, queued input |
| session | agentsession format (RFC 0001, 0002) | **record**: entries over Items, hashed, branched, verifiable; config, decisions, dispatches, grants, folds |

ACP is a projection of the turn: prompts, streamed chunks, tool calls,
permission requests and cancel. `front/acp` serves it, and drops the
record. openresponses is the model's wire, a layer below the turn. A
client built on it would rebuild agentturn inside itself. agentturn#80
records why `front/responses` cannot serve a person.

What was missing was a way to watch the record as it is written. That
is agentsession's `Follow` (`agentsession/docs/plans/follow.md`), and
this plan depends on it.

## The rule

**The record is the truth for everything committed. Live events carry
only what is not committed yet.**

A client renders a conversation from session entries, and overlays the
in-flight events until each one lands as an entry. The recorder already
draws the line:
- `item_start` and `item_update` write nothing;
- `item_end` writes the item;
- `tool_dispatch` writes the dispatch before the tool runs;
- `tool_end` writes the output;
- `response_end` writes the response.

So the overlay is exactly:
- the text and reasoning streaming in;
- a tool call opened and not yet ended, with its progress;
- the state of the turn: running, requires action, idle;
- a permission request that is out.

Reconciliation is by key. An overlay item is dropped when an entry
lands carrying it, matched on the item's ID within its response, or on
the call ID for a function call or its output. Two lags in the recorder
mean the overlay must wait for the entry rather than for the event:
- a config change is written by the next entry-writing event;
- a model's item that completes before the stream names its response
  is written at `response_end`.

The overlay holds an item until its entry lands, and a run's end
flushes whatever is left.

This gives one rendering path for a live run, a history, a replay, a
fork and a resumed session. The record carries the transparency
without a second view: branches, `Verify`, policy verdicts, skill
grants, memory manifests and folds are all entries.

## The contract

An internal package, `internal/client`, written against the stack's own
types:

```go
// Backend is one agent the client drives.
type Backend interface {
	Control() Control
	// Live streams the uncommitted part of the agent's activity:
	// deltas, open tool calls, turn state, permission requests.
	Live(ctx context.Context) iter.Seq2[LiveEvent, error]
	// Record follows the agent's session. Verified reports whether
	// the record is the agent's own, hashed and checkable, or one
	// the backend synthesized from what it was told.
	Record() Record
}

type Control interface {
	Prompt(ctx context.Context, items ...openresponses.Item) error
	Steer(items ...openresponses.Item)
	Abort()
	Answer(ctx context.Context, answers ...agentturn.Answer) error
	State() agentturn.State
}

type Record interface {
	Follow(ctx context.Context, from agentsession.Cursor) iter.Seq2[agentsession.Change, error]
	Verified() bool
}
```

`LiveEvent` is a narrowed view of `agentturn.Event`: the events that
carry no committed fact. The contract grows as the TUI needs it, and
`SetConfig` and forking are added when a feature needs them.

## Backends

1. **Native, in process.** An agentkit `Kit` or an `agentturn.Agent`,
   with its recorder's store. Control is the agent's. Live is
   `Subscribe`, filtered. Record is `Follow` on the same store value,
   which wakes on each append with no polling. This is the first
   backend and the reference for the others.
2. **ACP client.** A foreign agent over ACP (stdio or a pipe). Control
   maps to `session/prompt`, `session/cancel`, and the answers to
   `session/request_permission`. Live maps from `session/update`.
   Record is synthesized from completed chunks and tool calls: one path,
   no hashes, no decisions. `Verified()` is false, and the TUI says so
   on screen.

   This is the consume side of ACP. agentturn's composition rule says a
   protocol earns its place with both sides, and `front/acp` is the
   serve side. The ACP client may belong in agentturn later, as the
   piece the rule asks for. It starts here, because only this client
   needs it.
3. **Native over a wire**, later. A daemon (dexclaw's shape) serves
   Control, Live and Record to a remote client. Choosing the wire waits
   for this backend:
   - the record half is RFC 0001 JSONL lines;
   - live and control can reuse ACP's method names where they overlap,
     so speaking ACP stays cheap;
   - openresponses' `sse` and `websocket` packages are the transports
     at hand.

## What the TUI shows

- **Conversation:** the path's messages, reasoning, and tool calls with
  their outputs, from the record, with the overlay streaming in.
- **Turn:** running, waiting on a permission, or idle. Permissions are
  answered in place, as allow once, reject, or edit and allow where the
  backend allows edits.
- **Tree:** the session's branches and forks, with switching between
  them. A fork's origin is reached through `parent_session`.
- **Record detail:** `Verify` status per response; each call's policy
  decision and the rule behind it; skill grants in force and when they
  ended; the memory manifest; folds and what each one dropped.

A backend without a verified record shows the conversation and turn
views only, labelled as unverified.

## Order of work

1. **agentsession `Follow`**, per its plan, released before this repo
   requires it.
2. **The contract and the native backend,** tested headless: a scripted
   model, a store, and assertions on what the client would render,
   including the reconciliation lags above.
3. **The TUI:** conversation and turn views first, then the tree, then
   record detail.
4. **The ACP client backend,** tested against `front/acp` over a pipe,
   so both sides of ACP are exercised by stack code.
5. **Extract the contract** once the wire backend or a second client
   needs it.

## Open questions

- **The terminal toolkit.** The likely choice is Bubble Tea. It needs
  to be picked, along with its dependency weight, before step 3.
- **Edit-and-allow over ACP.** ACP's permission is allow once or reject
  once. A reviewer's argument edit, which agentpolicy supports, has no
  ACP shape. It shows only on native backends until ACP has one.
- **The repository's name.** `agentconsole` is a working name. It is
  fixed before the first push, since it is the module path.
