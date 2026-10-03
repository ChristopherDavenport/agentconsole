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

## Decided in implementation

Step 2 (the contract, the native backend and the reconciler, tested
headless) changed the sketch in these places.

- **Prompt and Answer block.** They return when the run ends, as
  `Agent.Prompt` and `Agent.Resume` do, with the error that kept the run
  from starting or ended it. A TUI calls them from a goroutine and reads
  the outcome from the live `RunEnded`. `Steer` and `Abort` return at
  once.
- **Live subscribes when called.** A lazy iterator would subscribe when
  first ranged over, and a client that starts a run right after calling
  `Live` could miss its first events. Events queue without bound for the
  one reader: the agent delivers as a barrier, and a slow terminal must
  not stall the run or the recorder.
- **LiveEvent is a set of concrete types** (`RunStarted`, `ItemOpened`,
  `ItemUpdated`, `ItemCompleted`, `ResponseCompleted`, `ToolOpened`,
  `ToolDispatched`, `ToolProgress`, `ToolFinished`, `RunEnded`, and two
  for turns), each a copy. `item_end` stays a live event even though the
  recorder writes it: the entry lags the event (the two lags), so the
  overlay needs the completed item until the entry lands. The agent's
  item is its accumulator and keeps changing, so the copy is taken in the
  subscriber, which the barrier makes safe.
- **A permission request is not an event of its own.** It is a
  `ToolFinished` with `Deferred` (carrying the question), settled by the
  `Pending` list on `RunEnded`, which is authoritative.
- **tool_end does not write the output.** The output is an item entry
  written at its own `item_end`, after the batch, in the call order. The
  call's overlay therefore lives from `tool_start` until the output
  entry lands, and carries the result in between.
- **Run end flushes on both halves.** The live `run_end` can beat the
  followed entries, so the overlay is flushed when the record's run end
  entry has landed as well, never on the event alone. A run that never
  writes its end entry is flushed by the next run's start.
- **Order independence.** An entry is written before the live event that
  follows it is queued, so the follower can deliver the entry first. The
  view remembers what has landed (items by ID, calls and outputs by call
  ID) and ignores a late live event for it.
- **Items with no ID are not overlaid.** Only the loop appends them (a
  prompt, a steered message), and the recorder writes them in the barrier
  of their `item_end`. A function call output is not overlaid either: its
  call row carries the result.
- **The two models.** The record's model in force and the turn's model
  are reported apart (`Model.Config`, `Turn.Model`), since a config entry
  is written by the next entry-writing event.
- **Branch tips are not `Session.Leaves`.** That list holds a branch's
  last run end, and a leaf label, as childless entries. The view reads
  each as the item the branch rests on, and counts a branch once.
- **Tests use a jsonl store.** The memory store shares its entries with
  its followers, and the recorder sets a config entry's ID after
  appending it, which the race detector reports (see the findings in the
  build report).
- **Pending calls come from the record, live events refine them.** The
  run end entry lists the calls left pending, and
  `Session.PendingCalls` (agentsession v0.0.21) reads the ones without
  an output with no tools. A view takes them on a snapshot, a reset, a
  head move and each run end entry: a deferred call (its latest decision
  is a hold) of an input_required run is a permission, with the hold's
  reason as the question; any other is cut off. A view attached to a
  session already at input_required therefore shows the permission with
  no live event. The record's last run speaks only when the live stream
  has said nothing of a later one and no run is going. (An earlier note
  here said nothing exposed this; `PendingCalls` does.)
- **Cut-off calls are not questions.** A call an abort or a failure
  left unanswered is `CutOff`, with its own call state.
- **Response IDs are compared strictly.** A row whose stream has not
  named its response is identified by run and turn, and matches an entry
  only while that entry's response is open. A response ends when its
  live end arrives or its response entry was in the snapshot.
- **A row still streaming when its response ends is dropped.** Only
  `item_end` commits, so a message a guard withheld, or one a failed
  response cut off, has no entry coming.
- **Items kept from the model are rows.** The recorder writes an item
  its filter hides from the model as a custom entry. It is on the
  record, so it is rendered, flagged `KeptFromModel`.
- **Branch tips and the leaf rest on items.** A label, a run end and a
  response stand for their parent. The path is read to the end of the
  bookkeeping behind the leaf, so run ends and decisions after the last
  item still inform the calls and the turn.

- **The run-end memory is capped at 64.** The view remembers the run end
  entries the record delivered ahead of the live stream's `RunEnded`, to
  settle a run when the second half arrives. The set holds 64. What is
  lost past it: a run whose end entry was evicted before its live end
  arrived is not settled by that entry, so its leftover overlay rows stay
  until the start entry of a later run lands. That needs the record to be
  more than 64 runs ahead of the live stream, which the native backend
  cannot be (the entry is written before its event is queued, and both
  are consumed in order). The cap exists for sessions another process
  writes, where no live end ever comes and the set would otherwise grow
  for ever.

- **Response identity changed after the interleaving test.** Marking a
  response over by its response entry was wrong: the record can be a whole
  run ahead of the live stream, with the entry and the response entry in
  before the stream's first event for the item. The view now settles a
  response only on the live stream's end of it (`closed`), and matches a
  live item that has no response yet to an entry by ID and text: what the
  stream has is the start of what the entry holds, and an entry in a
  response the stream ended is not the item's. A reused ID with other text
  is another item; the same ID and the same text in a response the stream
  never saw is taken for landed until the stream names the response.
- **What waits is decided from the viewed path, always.** The record's
  last run on the viewed line decides, and a live run end stands in for it
  only until the record delivers its end entry (`reconcilePending`, run on
  every record change and every live run end). A live end for a run the
  viewed path has moved off says nothing about the viewed line.
- **A run's start entry settles an earlier run only if the stream saw it
  begin later** (`runSeq`), not any start entry: a reset replays older
  starts.
- **The interleaving test** (`internal/view/prop_test.go`) plays scripted
  runs as the agent and recorder would, delivers the record's changes and
  the live events in random interleavings with resets and head moves, and
  checks the view after each step. `AGENTCONSOLE_SEEDS` and
  `AGENTCONSOLE_SEED0` run more seeds or replay one.

- **Known limit: a reused ID, an unnamed stream and a snapshot attach.** A
  live item with no response yet is matched to an entry by ID and text. A
  view attached from a snapshot that holds an earlier response the stream
  never saw, with a new item that reuses its ID on an unnamed stream whose
  text starts like the old one, shows nothing until the text diverges, and
  drops a completed row equal to the old text until its own entry lands.
  All four conditions are needed, and it ends when the response is named or
  its entry lands. It is cosmetic and transient, and the fix would be a
  turn number on the record's response entries, which the format does not
  carry.
- **Live rows belong to their run's line.** The view follows each run's
  own line through the entries that land and shows the run's overlay rows
  only while the viewed line extends the run's newest entry, so a head
  moved back does not show another branch's streaming row under the path,
  and the row returns with the view.
- **The property test sees liveness and call state.** Every item the
  stream opened whose entry has not landed must be shown, on the line it
  belongs to and no other; call rows are checked for state, output,
  progress and children; the turn's number, model and attempt are checked.
  The generator plays what the recorder does: reasoning ended when the
  loop commits, a stream that names its response part-way, a cut stream
  whose response has no ID, a failed response, marked and hidden items,
  bookmarks, head records without an entry, progress, child calls and
  steered input, and a recorder that settles the config late.

## Decided in implementation, step 3 (conversation and turn views)

- **The toolkit is Bubble Tea**, with `bubbles` (viewport, textinput) and
  `lipgloss`, and nothing else direct: bubbletea v1.3.10, bubbles v1.0.0
  (which requires exactly that bubbletea and lipgloss v1.1.0), lipgloss
  v1.1.0. Why these: they are the de facto Go TUI stack, their Update and
  View are plain functions, so the model is tested headless by sending
  messages and reading `View()`, with no teatest; and the transitive
  weight is the terminal basics (termenv, x/ansi, runewidth, uniseg). No
  testing dependency was added.
- **The view is fed under one lock, and the model is taken inside it.**
  `tui.Attach` follows Live and Record on two goroutines. The view is not
  safe for concurrent use, and a `Change.Session` is valid only until the
  follow takes its next step, so the change must be applied before the
  iterator resumes: it cannot be handed to the program's goroutine. Each
  step is applied, the `view.Model` taken (it shares nothing with the
  view) and sent to the program as a `ModelMsg` inside the lock, so the
  program sees the models in the order they were applied.
- **Prompt and Answer run in tea.Cmds.** The model keeps its own `busy`
  flag from the moment the command starts until it returns, since the
  view's turn state lags at both ends. Enter prompts when idle and steers
  when busy or the view says running; Ctrl-C aborts a running run (a second
  one quits, in case the abort never ends the run) and quits when idle.
- **Permissions are answered one at a time, sent together.** Each
  permission out is asked in turn (y approves, n asks for an optional
  reason and Enter refuses); once every one has an answer, one
  `Control.Answer` carries them all, since a resume has to settle what
  is pending. A refusal is `agentturn.Refuse`, so it ends the run without
  calling the model, and its output says the user refused and why. Answers
  are recorded `By` "human".
- **Keys.** The conversation scrolls on PgUp, PgDn, Ctrl-Up, Ctrl-Down,
  Ctrl-Home, Ctrl-End and the wheel; the arrows, Home and End edit the
  input line. SIGINT and SIGTERM from outside are an `InterruptMsg`, the
  same as Ctrl-C (the program's own handling is off).
- **A feed that stops is not restarted.** The status line says so and
  says to resume the session; a new feed would start from a new view and
  a Live subscription that misses what the run emitted meanwhile.
- **Quitting waits for the run's write.** After the program ends, main
  aborts and waits up to three seconds for the in-flight Prompt or Answer
  (`Model.Drain`) before cancelling and closing the store.
- **Collapsing is global.** Ctrl-R shows or hides all reasoning, Ctrl-O
  shows tool arguments and output in full (three lines and 120 columns
  otherwise). No per-row cursor yet.
- **The binary.** `cmd/agentconsole` runs an `agentturn.Agent` in
  process. `--session ID` or `--session ref:NAME` resumes,
  `--conversation NAME` resolves or creates the ref with
  agentsession's `SessionFor` and resumes the session, neither starts a
  new one. A resumed agent is seeded with `session.AgentOptions`. Tools are
  none or a clock; `--confirm-tools` defers every call so the permission
  path can be driven by hand. agentkit integration (real tools, policy,
  skills, memory) is a later step.
- **What the contract and the view did not carry.**
  - A hidden item never reaches the view, so a row cannot be shown as
    hidden: only `KeptFromModel` rows are distinguished.
  - A live reasoning row shows no text while it streams (its length reads
    0 until the entry lands), so a streaming reasoning row shows only that
    the model is thinking.
  - `Model.Rows` is the whole path on each step and the TUI renders all of
    it on each step; a long conversation will want a per-row cache.

## Open questions

- **Edit-and-allow over ACP.** ACP's permission is allow once or reject
  once. A reviewer's argument edit, which agentpolicy supports, has no
  ACP shape. It shows only on native backends until ACP has one.
- **The repository's name.** `agentconsole` is a working name. It is
  fixed before the first push, since it is the module path.
