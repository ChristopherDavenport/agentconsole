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

A package, `client`, written against the stack's own
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
   needs it. Done for the first embedder, dax: see "Decided in
   implementation, step 5 (the library)".

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
- **The interleaving test** (`view/prop_test.go`) plays scripted
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
  testing dependency was added. (Since moved to v2: see "Bubble Tea
  v2".)
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
- **Collapsing is global, or the selected row's.** Ctrl-R shows or hides
  all reasoning, Ctrl-O shows tool arguments and output in full (one
  line, the arguments compact, otherwise). With a row selected they flip
  that row alone: a row keeps which switches it has the other way round
  from the global ones, by entry, and toggling a switch with no row
  selected drops every row's flip of it, so a global toggle leaves the
  rows alike.
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

## Decided in implementation, step 3 (the tree and record detail)

- **Viewing is local; continuing moves the head.** Opening a branch in the
  tree reads the session and renders the line ending at its tip
  (`view.At`), as a frozen read-only view beside the live one. The agent's
  head does not move, the feed keeps running and Esc returns to the live
  view. The head moves on an explicit action, "continue from here" (`c` on a
  branch in the tree or in a frozen view, `ctrl+b` on the selected row),
  which is the contract's new `Control.ContinueFrom(entry)`. The next
  prompt continues from that entry and the live view follows the head,
  since the follower reports it.
- **How the native backend moves the head.** `Recorder.Rebase` onto the
  entry (it reseeds the recorder and reserves call IDs, and closes a run
  the entry sits inside as interrupted), then the agent is given the
  branch's transcript, pending calls and reasoning attribution
  (`SetTranscript`, `SetPending`, `ContextWithReasoningModels`), and a leaf
  label is appended. agentsession v0.0.21 has no head move on its `Store`
  interface (the cas store keeps a head, nothing on `Store` sets it, and
  `Rebase` moves the writer's session leaf without recording anything), so
  the leaf label is the only durable form, and it is what a follower and a
  reopened session read. It is refused while a run goes. Another session
  (an origin, a subsession) can be read in the client and not continued:
  that is `--session`.
- **The contract grew by three methods.** `Control.ContinueFrom`,
  `Record.Read(ctx, id)` (a session whole, as the caller's own: the panes
  and the read-only views are computed from snapshots, never from the
  follower's session) and `Record.Refs(ctx, id)` (the refs that point at a
  session; `ErrNoRefs` when the store keeps none).
- **What the view added.** `Model.Branches` (each tip labelled by its
  newest message, its run and its time; newest first), `Links` (the
  session's link entries: subsessions, the successor), `Origin` (the
  header's parent session, base and spawning call), `Tail` (the end of the
  viewed line, which the panes read the path to) and `Entries`;
  compaction entries are rows (`Row.Fold`); `view.At` renders any line of a
  session read-only. `Leaves` keeps its meaning. `Branches` does not use
  it, since a tree needs fewer tips: a tip that is an ancestor of another
  is not a branch (after a head moved back and extended, the old line's
  response and run end still end on the entry, childless, as does the
  leaf label that marks the move), and the viewed line is always one.
- **Branches, forks and links in the tree.** The tree lists the branches,
  then the origin (`parent_session`, with the `base` entry, from the
  header, and the session's own link entries for subsessions and
  `continued_in` successors). Opening the origin shows it read-only at its
  own head with the cursor on the fork point, or at the fork point when
  the origin has gone on elsewhere. Forks *made from* this session are not
  listed: nothing on the session says so (its link entries are only for
  subsessions and successors), and finding them is a store `List`.
- **Detail is computed on demand, off the feed.** `internal/inspect` reads
  a snapshot (`Record.Read`), reused while the session holds no more
  entries, and computes one entry's detail or the session summary in a
  `tea.Cmd` only when a pane is open and what it shows changed: another
  row, or a call still moving. A response's `Verify` is cached for good
  (its entry and the path behind it never change), so the session
  summary's check over the path costs one `Verify` per new response. While
  a run goes the session pane keeps what it shows and refreshes when the
  run ends, so it is not recomputed per entry. Two panes asked for at once
  read the session once.
- **What the detail says.** For a row: the entry's ID and time; for a call
  its decision entries (verdict, `by`, reason, replaced arguments), the
  policy verdict records written for it (`agentpolicy:verdict`: action,
  rule, source, note), the questions put to the user
  (`agentturn:elicitation`), its dispatch, its output and the skill grants
  in force when it was made, and whether one was revoked later; for the
  response behind an item its model, status, usage, latency, attempts and
  `Verify`: verified, **unhashed** with the cause the record names (the
  nearest `agentturn:unhashed` entry behind it), or a mismatch; for a
  compaction what it folded (the entry kept from, how many items before it
  stay on the record, the summary's length and start, the pinned items, the
  tokens before, the fold's own model call). The session pane: id, name,
  cwd, harness, format, origin, the tally of `Verify` over the viewed line
  with the responses that did not verify, the viewed line's token usage
  (by model, and its cost when a price source is supplied), the config in
  force (model, instruction parts, tools), the refs that point at it and
  the memory manifest in force.
- **Other products' records are decoded here, from their JSON.** The
  client depends on none of agentpolicy, agentkit or agentmemory. It reads
  `agentpolicy:verdict` (the shape in agentpolicy's `record.go`), takes a
  skill grant to be an allow verdict whose reason starts "granted " from a
  source named `agentskill:...` and its revocation to be the verdict whose
  reason starts "revoked the rules granted by " (agentkit v0.0.7), and
  folds `agentmemory:render` records, whole or delta, as agentmemory's
  `ManifestFold` does (the bases among the last eight manifests in force).
  The reasons are text conventions of those products and not a format; a
  change there silently empties the grants line. A record that does not
  decode is left out of the pane.
- **The root session's header names the client.** `session.WithHarness`
  names the writer only in the headers of child sessions; the binary now
  puts the harness and the working directory in the header of a session it
  starts (the summary pane showed neither).
- **Keys.** `ctrl+/` (or `f1`) shows the list of keys and takes no input
  until `esc`, `q` or `ctrl+/` closes it. A terminal that speaks the
  kitty keyboard protocol names Ctrl-/ itself; the rest send it as the
  unit separator, which bubbletea names `ctrl+_`; Ctrl-? (Ctrl-Shift-/)
  comes the same way in most terminals and as DEL in the rest, where it
  cannot be told from backspace, so F1 opens it too. `ctrl+t` switches
  between the conversation and the tree (in
  the tree: up/down or k/j, `enter` views the branch or opens the origin,
  `c` continues from the branch, `esc` goes back). In the conversation
  `ctrl+p` and `ctrl+n` move a row cursor (a marker in the gutter; `esc`
  clears it), a left click puts it on the row under the pointer and a
  click on the row it is on expands or collapses that row (the rows'
  places come from the last layout, so the click lands on what was
  drawn), `tab` cycles the pane under the conversation: the selected
  row's detail, the session summary, none; `ctrl+b` continues from the
  selected row; in a read-only view `esc` returns to the live session, `c`
  continues from the cursor row or the viewed branch's tip, and the input
  is off.
- **ContinueFrom validates, refuses and undoes.** It refuses a leaf label
  and an entry on a fork's prefix above the base before anything moves
  (agentsession's `mayRestOn` is unexported, so the rule is repeated), and
  an entry that leaves function calls without an output: the agent would
  hold pending calls nothing in the client can answer. A failure after
  `Rebase` puts the leaf, transcript, pending calls and reasoning
  attribution back and appends a leaf label. `Rebase` into a run still
  appends that run's interrupted end. The run check and the move are not
  atomic (agentturn has no lock to hold across them); the TUI is busy for
  the whole move, and `Rebase`/`SetTranscript` refuse once a run has
  started.
- **Revocations and manifests follow their writers' checks.** A
  `revoked the rules granted under <scope>` (or `without a scope`) verdict
  ends every grant, as agentkit reads its own scope (a grant's verdict has
  no scope of its own). A manifest delta is refused, and shown as refused
  with why, unless its result hashes to the hash it states and its
  elements are well formed. Grants in force for a call are measured at its
  last decision, not its item.
- **Not done.** Per-row collapse is still global. A forks-of-this-session
  list needs the store's `List`. A call's dispatches on a fork's origin
  (`agentsession.OriginDispatches`) are not followed in the detail. The
  pane is clipped to half the screen and does not scroll.

## Decided in implementation, step 5 (the library)

dax, a coding agent in its own repository, is the second consumer: it
imports agentconsole and runs the client over its own agentkit kit.

- **The public surface.** `client`, `client/native`,
  `client/kitbackend`, `view` and `console`. `internal/tui` and
  `internal/inspect` stay internal: nothing an embedder does needs a
  Bubble Tea model or a record pane, and `console.Run` is the one entry.
  `view` is public because it is the contract's other half (a different
  client, a web one, renders `view.Model`), not because `console` needs
  it. The packages moved by `git mv`, so their history follows, and
  every import path changed once, before there was a release.
- **`console.Run(ctx, backend, ...Option) error`** is the body of the
  binary's `run`, moved: build the model, start the program with the
  alt screen and the mouse, turn SIGINT and SIGTERM into
  `tui.InterruptMsg`, `tui.Attach` before `p.Run`, and after it ends
  `Abort`, `Drain`, cancel, `Drain`, wait. An embedder gets the same
  behaviour, including that Run returns only once the run it was driving
  has written its end, so closing the store behind it is safe. It does not
  print the session ID (the binary does): `Backend` has no session ID, and
  an embedder knows its own. Options are the ones a test or an embedder
  needed and nothing more: `WithInput`, `WithOutput`, `WithWindowSize`
  (a pipe reports no size, and the program draws nothing until it knows
  one), `WithDrainTimeout`, `WithWarn` (the one warning Run has), and
  `WithoutSignalHandler` (an embedder that owns signals). The alt screen
  and the mouse are not options: they are the client.
- **The window size is sent from a goroutine.** `Program.Send` blocks until
  the program runs, so sending it inline before `p.Run` deadlocks.
- **Why `client/kitbackend` and not `native.FromKit`.** agentkit imports
  every library of the stack, an MCP client and a policy engine among
  them. `native` needs the agent and the recorder, which agentconsole
  already required, so an embedder that builds its agent by hand pays
  nothing for a backend over a kit. agentconsole depends on agentkit as a
  module, which is cheap; what a package pulls into a build is the cost
  worth keeping out. The kit backend embeds `*native.Backend`, so the
  contract, `Agent()` and `SessionID()` are one implementation, and adds
  what the kit expects of a front.
- **`native.New` takes options, `WithRunContext`, `WithRelease` and
  `WithHeadMove`,** instead of the kit backend reimplementing `control`.
  They are the three places a host's state meets Control:
  - the run context: every `Prompt`, `Answer` and head move runs under
    `agentkit.ContextWithRecorder(rec)`, `session.ContextWithSessionID`
    and `agentmemory.WithSession`. With the recorder on the context the
    kit's grant scope is the session's ID (`Kit.GrantScope`), the verdicts
    and a tool's questions land in the session the client follows, and a
    skill read is granted to this conversation. A conversation is one
    session, so the scope never changes under a `ContinueFrom`: a branch
    is not another conversation.
  - `Answer`: the backend keeps the `RunEnd` of the last run that
    returned one, and `Answer` passes it with the answers to the kit
    engine's `Release`. That releases the calls the engine only held for
    another call's ask (the TUI answers the calls it shows, and the
    engine holds the rest), and turns a call nobody answered into
    `ErrUnanswered`, which names it, instead of the loop's own error. The
    end is used only if an answer is about one of its calls: pending calls
    restored from a record after a restart belong to no run of this process
    and are released with `end == nil`, which returns the answers as given.
    A run that fails before it begins leaves the earlier end in place; a
    head move clears it.
  - `ContinueFrom`: a skill's grants follow the path, so the move ends them
    before the recorder leaves the branch (`Kit.RevokeSkillGrants`, which
    records its revocation on the branch being left) and grants again what
    the new branch's reads granted after the transcript and the pending
    calls are set (`Kit.RegrantSkills` on the session at the new leaf). The
    order matters: a revocation written after the move would sit on the new
    path and the replay would read it as ending the grants it is about to
    make. The undo of a failed move runs the same pair the other way, with
    the revocation at the new head before the rebase back, so it is on a
    side branch, and the regrant at the old one. A failing before hook
    refuses the move.
- **The elicitor is the embedder's.** `agentkit.WithToolElicitor(by, fn)`
  takes the function that answers a tool's question; the kit files the
  question and answer under the call, in the recorder of the run's context,
  which the backend puts there, and the detail pane shows them. The
  embedder's `fn` puts the question to the client through `Ask` (below).

## Decided in implementation, step 6 (questions)

- **A question is asked while the run goes.** A sub-agent's call its
  policy holds, or a tool's own question, waits inside a running call;
  the run cannot end input-required and resume, as a permission does,
  without cutting the call off. So a question is its own thing in the
  contract: `client.Question` (an ID, the call it is about when there is
  one, the text) and `client.Reply` (accept, and a note). The extension
  the step 5 note left waiting is this one.
- **The host asks through the backend.** `native.Backend.Ask(ctx, q)`
  blocks until a client replies or ctx ends; `kitbackend` has it by
  embedding. It is not on `Control`: the asking side is the host's code
  (dax's sub-agent policy, its tool elicitor), not the client's. The
  answer is: `Control.Reply(id, r)` returns at once, since the call goes
  on by itself.
- **Questions travel as live events and are not recorded here.**
  `QuestionAsked` and `QuestionClosed` (with the reply, or nil when the
  asking call gave up, an abort) reach every `Live` subscriber, and a
  subscriber that attaches while one waits is sent it, unlike past agent
  events: it still wants an answer. What the user decided is the asker's
  to record, as a decision of the sub-agent's call or the kit's filed
  elicitation; the client shows it from the record like any other.
- **Their `Run` is empty.** The asker may not know the run, and a
  question is answered by its ID; nothing filters live events by run.
- **The TUI asks a question ahead of a permission**, on the conversation,
  while the run goes: `y` allows (or says yes, for a tool's own question),
  `n` refuses with an optional reason typed in the input, `Esc` goes back.
  A reply hides its panel at once; the close event, when the view shows
  it, drops the question.
- **Resume is the kit's.** An embedder passes a kit built with
  `WithResumedSession`; `kit.AgentOptions()` seeds the agent with the
  transcript and the calls pending at the leaf, and `New` already granted
  again the skill grants the session's reads made. The backend adds
  nothing: a pending call is in `Control.State().Pending`, so the client
  asks about it as it does a live one, and the answer, with no `RunEnd` of
  this process, resumes it.
- **`inspect` reads a product's own skill source names.** dax names its
  grant sources `skill:NAME`, not agentkit's default `agentskill:NAME`, and
  the pane showed no grants. A source is now a skill's when it has the
  default prefix, or when its last segment is the name of a skill the path
  holds an `agentskill:read` record of.
- **Not done.** `kitbackend.New` needs a session; a kit with none has no
  record to show. Under `agentkit.WithRecorder` the owner of the recorder
  attaches it (the kit does not), to `Backend.Agent()`, before the first
  run. The binary still builds its agent by hand over `native`; moving it
  onto a kit would add flags for every kit option and no behaviour.
  Releases are cut with `make release`; the guard accepts any `vX.Y.Z` as
  the first release.

## Decided in implementation, step 7 (selection and copy)

The client took the whole screen and the mouse with it (cell motion
tracking, for the wheel and the row click), so nothing on the screen was
selectable: the terminal's own selection cannot reach a program that has
the mouse, and which modifier bypasses mouse reporting (shift in xterm,
option in iTerm2) is the terminal's choice, not the client's. The client
selects itself.

- **A left drag is the client's own selection.** The press anchors a
  cell; motion with the button down extends the region, in reading
  order (whole lines between the first and the last, columns on those
  two, the topmost cell first whatever way the drag went); the release
  ends the drag and leaves the selection drawn. A click, a press and a
  release with no motion between, still selects the row under it and a
  second click on that row expands it, as before: only the release acts,
  so a drag never selects a row.
- **The region is of the frame, not the model.** The selection is cut
  from the lines the last `View` rendered, so it covers anything on the
  screen — the conversation, the panes, the tree, the status line — and
  not the conversation's rows alone. Cutting a rendered line at display
  columns keeps the escape sequences where they sit (they carry no
  width), and a wide rune or a combining mark cut at its boundary stays
  whole.
- **Copy is a choice, ctrl+c, as a desktop's copy.** The terminal's own
  copy chords (Cmd-C, Ctrl-Shift-C) are the terminal's, for the
  terminal's own selection, and never reach the client; ctrl+c is the
  one copy key the client sees. With a selection drawn it sends the
  region's plain text to the clipboard with OSC 52, the one channel a
  program in the alternate screen has to the clipboard (it works over
  ssh too), and drops the selection, so the next ctrl+c is the interrupt
  again: a selection makes the first ctrl+c the copy and the second the
  abort or the quit, the rhythm a second ctrl+c already had; while a run
  goes the copy still comes first, or it would be unreachable mid-run.
  A signal from outside (SIGINT, SIGTERM) always interrupts. The copied
  text is the region's plain text, escape codes stripped and each line
  trimmed of the padding the layout adds, so a dragged status line
  copies its words, not its spaces, and the status line says "copied".
  A terminal that does not take OSC 52 (iTerm2 needs "Applications in
  terminal may access clipboard" on) silently copies nothing; the note
  is still "copied", since the client cannot ask a terminal whether it
  took the sequence.
- **The selection stays drawn**, reversed like a terminal's own, until
  the next key or the next press. It is not cleared by the frames under
  it: a streaming conversation reflows under the selection, as a
  terminal's own does.
- **Where a copy goes is the host's.** `tui.WithCopier` sets it
  (without it a selection is drawn but nothing is copied, which is what
  a test wants) and `tui.Clipboard(w)` is the copier over the writer the
  program renders to: the OSC 52 sequence is a command the terminal
  executes, not output, so writing it beside a frame cannot corrupt the
  frame. `console.Run` passes the program's own output writer, or
  standard output, so an embedder's test sees the sequence in the buffer
  it reads.
- **Not done.** A selection that scrolls the viewport when the drag
  reaches the screen's edge, and extending one with shift-click, are not
  there.
- **Later: the selection is held on the text.** Held on the screen, a
  selection on the conversation lost its text as soon as a run
  streaming below scrolled the conversation up, and ctrl+c copied
  whatever had come under it. An end of the selection on the viewport is
  now kept as a line of the viewport's content, so the selection moves
  with its text (a run streaming, the wheel), is drawn only where the
  viewport shows it, and a selection with both ends there copies from
  the content, the part scrolled out of sight too. An end anywhere else
  (the status line, the panes, the input) stays a line of the screen,
  and a selection with one there is cut from the frame as before. A
  line number follows a scroll and text added below, not a row inserted
  above (the prompt's row landing after the answer started streaming,
  in the first moments of a run) nor a rewrap at a resize: those move
  the text and leave the selection. Holding an end by its row and a line
  within the row would cover them.

## Decided in implementation, step 9 (clicking into things)

- **A click acts on what the last frame showed under it**, as the row
  click did: on a row it selects the row and again expands it; on an
  item of the tree it selects the item (the tree records each item's
  line as it renders) and again opens it, the same path as Enter; on the
  input it leaves the rows, as Esc does (the cursor and its pane go),
  and puts the input's cursor on the cell clicked. A drag is still a
  selection and never any of these.
- **The input's cursor goes to the rune under the pointer.** Past the
  text of a row it goes to the row's end; past the end of a row that
  soft-wraps it stops on the space the row wrapped at, since a cursor
  after it is on the next row. On a permission or question waiting for
  y or n the input is off and the click only leaves the rows.
- **The textarea's scroll is read from the frame.** (Superseded by
  bubbles v2, which keeps the scroll on the cursor as it updates and
  says where it is: see "Bubble Tea v2".) A value taller than
  the input's five rows scrolls inside the textarea, which does not say
  where; nor can the scroll be worked out from the keys, since the
  textarea clamps it against the content it last rendered, and when it
  renders is the program's frame rate (right after a paste it still
  shows the first rows, with the cursor below them). So the click
  matches the value's rows, wrapped with the textarea's own algorithm
  (the copy `wrapLine`), against the input's rows on the frame clicked;
  of two scrolls that both match (rows that repeat), the one showing
  the cursor's row. The cursor moves by soft-wrapped rows to the row
  clicked, which is shown, so the scroll does not move.

## Decided in implementation, step 8 (tool rows)

- **A collapsed call is one line.** The row of a function call showed the
  raw JSON of its arguments and three lines of its output by default,
  which for a coding session is the bulk of the screen: a read, a grep
  and a bash are most of what a model does. Collapsed, the row is now
  the tool's name and its arguments compact — the `key=value` pairs of
  the arguments object, sorted by key, each value clipped to 60 runes so
  the content of a write never takes the line — with the state suffix
  only while the call has not ended, and a hint of how many lines the
  output holds. The output is behind Ctrl-O or a second click on the
  row, which show the raw arguments and the whole output as before. A
  running call keeps a progress tail under the row's line — the last
  lines of what the tool reported, five by default, and all of them
  expanded — so a tool that streams its output (a running bash) reads
  live; a call that has ended shows only the count.
- **The arguments' key=value form is the client's, not the tool's.** The
  client knows no tool; it knows the arguments are a JSON object. A tool
  that wanted its own line (the command alone, say) would need a
  per-tool renderer, which the client would then have to grow per tool,
  and dax's print front has shown the generic form is readable enough.

## Decided in implementation, Bubble Tea v2

- **The toolkit is Bubble Tea v2**: `charm.land/bubbletea/v2` v2.0.10,
  `charm.land/bubbles/v2` v2.2.1 and `charm.land/lipgloss/v2` v2.0.6,
  under their new module paths. bubbletea v2.0.10 requires Go 1.26, so
  that is the floor now (v2.0.9 was the last on 1.25). No exported name
  changed: `console` never showed a bubbletea type, so an embedder that
  uses bubbletea v1 itself builds beside it.
- **The screen's modes are the view's.** v2 drops the program options
  for the alternate screen and the mouse; `View` returns a `tea.View`
  that asks for both on every frame. `console.WithWindowSize` is
  bubbletea's `WithWindowSize` now, rather than a size sent once the
  program runs. A program whose output is not a terminal and that was
  given no size reports 0x0 at its start, which the model takes for no
  size and keeps waiting on.
- **A paste is not a key.** v1 delivered a paste as a key message, so it
  went the way of keys; v2 has `PasteMsg`. It reaches the input only on
  the conversation and not read only, and it never answers a question
  or a permission: while one waits for y or n the input is blurred and
  takes nothing, until a refusal's reason is being typed.
- **The input's scroll is the textarea's.** bubbles v2 keeps the
  textarea's scroll on the cursor in every Update and says where it is
  (`ScrollYOffset`); the program draws after every Update, so at a click
  it is the scroll of the frame clicked on. The matching of the value's
  rows against the frame is gone. One quirk is the textarea's: rows
  deleted from the end leave the scroll where it was, with blank rows
  under the value, until the cursor moves up past them; the click lands
  on what is drawn.
- **Styles are always written; the program fits them.** lipgloss v2 no
  longer asks the terminal what it can show: a style always writes its
  escape codes, and bubbletea fits the colors to the terminal it writes
  to. The tests read the screen with the codes stripped. The textarea's
  styles depend on a dark or light background, which the model asks the
  terminal for at its start.
- **The tests that run the program read a terminal's screen.** v1
  redrew a changed line whole, so a test over a pipe could look for a
  word in the bytes written. v2's renderer writes only the cells that
  change between frames (moving the cursor, inserting characters), so a
  word streamed over two frames is seldom whole in them, which failed CI
  and passed locally by the timing. Those tests (`console`'s and
  `TestRunsUnderARealProgram`) now write the program's output into
  `internal/termtest`, a terminal emulator from `x/vt`, and read its
  screen; the clipboard, a command and not something shown, is still
  read from the bytes. This is the first testing dependency (step 3 had
  none). `x/vt` has no tagged release and is required at a
  pseudo-version; it is charm's own, built on the ultraviolet already
  in the graph, and only `internal/termtest` imports it, so a library
  build does not compile it.

## Open questions

- **Edit-and-allow over ACP.** ACP's permission is allow once or reject
  once. A reviewer's argument edit, which agentpolicy supports, has no
  ACP shape. It shows only on native backends until ACP has one.
- **A failed call on the record.** A tool that failed can be named on
  the live row (`ToolFinished.Err`), but the call's entry carries only
  the output the model saw, so once the entry lands the row cannot say
  the call failed. Distinguishing failed calls after the fact needs a
  field on the call's record, an `agentsession` change; until then a
  collapsed failed call shows only its line count like any other.
- **The repository's name.** `agentconsole` is a working name. It is
  fixed before the first push, since it is the module path.
