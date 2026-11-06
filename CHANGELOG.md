# Changelog

All user-visible changes to this library and binary. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
uses [Semantic Versioning](https://semver.org/); before v1.0.0 minor
versions may break the API.

## Unreleased

- Dependencies: agentturn and agentturn/session v0.0.38, agentsession
  v0.0.38, agentkit v0.0.17, agentpolicy v0.0.20, agenttool v0.0.30,
  openresponses v0.0.19.
- Changed: with agentsession v0.0.38 the binary raises every session it
  appends to to format 0.21, and so does an embedder that takes this
  release's floor by version selection (dax). A reader built on
  agentsession before v0.0.38 refuses those files until it is upgraded.
- Changed (#49): whose words a user message is comes from agentsession's
  `WordsOf` (format 0.21's trigger kind `human`), as the new
  `view.Row.Words` and `view.Queued.Words`. The TUI labels the person's
  words "user", a message the host composed by where it came from ("not
  your words"), now including one sent under another trigger kind with
  no layers or trust, and a message no trigger claims "user
  (unclaimed)". Sessions recorded before format 0.21 claim nothing, so
  their typed lines now read "user (unclaimed)"; lines sent over
  agentturn's front/control without `WithTriggerClaims` read as
  composed ("sent by control: not your words"). A live row reads as its
  entry will, a drained queued input's from its queued entry. A queued
  input's `visible`, `trust` and `instructions_part` are read from
  format 0.21's members first and agentturn v0.0.37's interim
  `agentturn:` members after, so a hidden owed input stays hidden after the bump; a copied
  input queued under the person's trigger (agentturn's `Queued.Copied`)
  is not shown as theirs.
- Changed (#50): `client.Direct` sends a steer through the control's
  `Accept` when it is an `agentturn.Accepter` (agentkit's `Control`,
  front/control's `Client`, a bare agent): it returns once the steer is
  queued, and a steer that misses the run's last drain is taken by a
  run the agent starts after it, where before it waited for the next
  prompt. A follow-up still goes through `Queue`. `Direct` sets no
  trigger: the embedder claims the person's words.
- Fixed (#50): `console.Run` aborts again after canceling its context
  at exit, so a run the agent started for a steer that missed the last
  drain, which the cancel does not end, is not left writing to the
  store the caller closes.
- Changed (#50): the binary claims what is typed at its terminal as the
  person's (a trigger of kind `human`, source `terminal`, on prompts and
  steers, and the answerer of a reply), so its sessions record typed
  lines as the person's words.
- Added (#51): the inspect panes read agentpolicy v0.0.20's `replace`
  verdict. A replacement of the policy ends the plain grants
  (`Engine.Grant`, `GrantOver`) as of its entry, "ended by the policy's
  replacement", and leaves grant sets in force; the session summary
  lists each replacement with its new default, the default's note, its
  sources and its reason, and a call's detail the replacement in force
  when it was decided.
- Added (#31): `console.WithStatus` makes up the status line:
  `StatusLine` with a position (`StatusTop`, `StatusBottom`, under the
  input's lower bar, its notices on a row just over it), a style (`StatusPlain`, `StatusPowerline`), the
  segments in order (built-ins `BuiltinSegments()`: time, tokens, cost,
  model, context, retry, turn, session, verified, stopped; and the
  host's, `Host` by name as `func() Segment`, "" hiding one), colours
  per segment with thresholds for the numeric ones (`SegmentColour`,
  `Threshold`), the window the `context` segment divides by (`Window`)
  and `Updates`, which redraws the line when a host segment changes.
  `Run` refuses a segment it does not know. Without the option the line
  is today's (`DefaultStatusSegments()`), except that a line too wide
  for the terminal drops whole segments rather than being cut at a
  column. `view.Model.LastCall` (`view.ModelCall`) is the viewed line's
  last model call with its usage, which the context segment reads.
- Added (#7): `console.StatusLine.Model`, the model the host runs next.
  The model segment names it from the start, with the record's model
  beside it until a run under the host's lands ("model B (record: A
  until the next run)"), so a resumed session shows the configured
  model before the first prompt. The binary passes its `-model`.
- Added (#43): Shift-Tab sends `client.NextMode`, an intent that names
  no mode, for the host to move to its next one and show it in a status
  line segment of its own. `client.Direct` refuses it with
  `client.ErrNoModes`; the refusal shows on the status line.

## v0.0.15 - 2026-11-04

- Dependencies: agentturn and agentturn/session v0.0.37, agentsession
  v0.0.37, agentkit v0.0.16, agentpolicy v0.0.19, agenttool v0.0.29,
  openresponses v0.0.18, agentmemory v0.0.12 (now direct).
- Changed: with agentsession v0.0.37 the binary raises every session it
  appends to to format 0.20, and so does an embedder that takes this
  release's floor by version selection (dax). A reader built on
  agentsession before v0.0.37 refuses those files until it is upgraded.
- Changed, breaking: `view.Run` holds an `openresponses.Usage`, which
  openresponses v0.0.18 made not comparable, so a `Run` can no longer
  be compared with `==`.
- Changed: the inspect panes read a skill grant from what agentpolicy
  v0.0.19 records as members (`grant`, `source`, `scope`), no longer
  from the reason: a revocation ends only its source's set under its
  scope, a scope's revocation only that scope's sets, and an activation
  replaces its source's set under its scope ("replaced at"). A grant
  shows the scope it was made under. A record written before v0.0.19 is
  still read by its reason.
- Added: a call the record holds a `result` for and no output (agentturn
  v0.0.36's `PendingReturned`: its tool returned and the process
  stopped before the batch's outputs were appended) is
  `view.CallReturned`, "ran; its result is recorded", with the result
  as its output. It is listed with the cut-off calls, never asked
  about, since agentturn's Resume refuses an approval of it; before, it
  read as cut off and may have run.
- Added: `view.Call.Failed`, an output the record marks `failed` or a
  tool that ended with an error live, shown as "failed"; and
  `view.Call.Withdrawn`, the reason of a dispatch withdrawn before the
  tool ran (a `reject` with `withdrawn`), shown as never started; a live
  `tool_end` marked `Withdrawn` says so at once, and is not a failed
  call. A live `tool_end` that agentturn marks `Cut` reads as cut off,
  not ended.
- Added: `view.Row.CopiedFrom`, the session a copied item (format 0.15
  `copied_from`) was taken from, shown as context copied from another
  session; and `view.Row.LeftOut`, `LeftOutQuarantined`,
  `LeftOutOmitted` or `LeftOutPlaceholder`, from the `omit` in force at
  the viewed line, so an output a guard quarantined is shown as sent to
  the model as the quarantine's placeholder, and a trimmed item as left
  out.
- Changed: answers to a running call's question name `agenttool.ByHuman`,
  as the permission panel's answers already do.
- Changed: the binary's model adapter leaves out other providers'
  slug-prefixed items (`openresponses.WithProviderSlugs()`), since its
  endpoint is a provider's own API.
- Changed: the session pane folds memory manifests with agentmemory's
  `ManifestFold` (v0.0.12) in place of a local copy that had drifted
  (#4). A manifest recording a block the product left out whole
  (`dropped`) hashes and folds as agentmemory's does, where the copy
  refused a delta carrying it, and reads "memory block left out this
  turn"; a refused record says whether its base was unresolved or it
  was malformed.
- Fixed: after a restart, a call its hold marks held (agentturn/session's
  `agentturn:held`), deferred only beside another call's question, is
  no longer asked about, and the asked call's question names the part
  of it the hold records (`agentturn:subject`), as the live end already
  did (#9). The view reads the hold decision's members, so no media is
  resolved and a sidecar session needs no blob reader for it. A held
  call is drawn deferred after a restart as on the live path, never as
  open or running, and `view.Call.HeldBeside` marks one held only beside
  another's question: "waits on its batch's question".
- Fixed: a call's detail in a fork shows the dispatch of a call in the
  fork's prefix, which the session the fork was made from holds
  (`agentsession.OriginDispatches`, read through the record): "dispatched
  in origin SESSION, off this path: may have run", with its key, where
  it said "dispatch: none recorded" (#5). An origin the store does not
  hold shows none; a chain of forks that does not end is shown. The
  call's state is read through the session, so a prefix call is no
  longer "never_started" under the fork's records promise.
- Added: `client.Lister`, an optional capability of a `client.Record`:
  `List(ctx, parentSession)` gives the summaries of the sessions whose
  header names the session as parent (agentsession RFC 0003, List).
  `RecordOf`'s record offers it.
- Added: the tree lists the sessions made from the followed one under
  "sessions made from this one", each with the entry it was made at,
  when the record can list them (#6): a listed session with a base, no
  spawned_by and no link of this session naming it, most likely a fork,
  since a header alone cannot tell a fork from a judge. Subsessions and
  children a host started stay with the links. Such a session opens
  read only.
- Added: `console.WithHistory(past, record)`: Up on the input's first
  line and Down on its last walk the lines sent, newest first, from the
  host's history on, and bring the draft back (#44). The host keeps the
  file; `record` is handed each line as it is sent, a slash line or one
  the submit function refuses included. A recalled line is sent only
  with Enter.
- Added: Ctrl-G opens the input in `$VISUAL`, or `$EDITOR`, on a private
  temporary file, and puts what was saved back in the input without
  sending it (#46). The file is removed afterwards; no editor or a
  failing one is said on the status line, and the key is refused while
  a question or a permission is answered. The editor must wait for the
  file (`code --wait`). What the input held when a refusal's reason or
  a question's note takes it over comes back after the answer, and
  after an intent's refusal that lands while a reason is being typed,
  which drops the half-typed reason as Esc does.
- Added: a message the host composed from text the person did not write
  (agentturn v0.0.37's `WithProvenance`: instruction layers, a source,
  trust) is shown as such, "composed from LAYERS, sent by SOURCE, LABELS:
  not your words", never under "user": on the record (the item's
  `instructions_part` and `trust`), live (`ItemStart`/`ItemEnd`'s
  `Provenance`, copied by `client.Copy`) and in the queued list (the
  queued entry's `agentturn:instructions_part` and `agentturn:trust`).
  `view.Row.Provenance` and `view.Queued.Provenance` carry it. Live and
  on the record alike a message is marked by its layers or its trust;
  a provenance naming only a source marks nothing, as the record cannot
  tell it from the run's trigger (agentturn#303). A queued
  input marked hidden (`agentturn:visible` false) is not listed, as its
  item is not shown once drained.
- Fixed (security): a key pressed as a permission or question panel
  opened was taken as its answer, so a `y` typed in the middle of a
  steer approved the call at once. A panel takes no answer for half a
  second after it opens: keys typed meanwhile stay with the input line,
  and Enter sends nothing until the panel is answered.
- Fixed: a head on the universal root is the empty line, never read as
  "no head" (the view now takes the follower's leaf as given).
- Dependencies: agentsession v0.0.28, up from v0.0.27, and agentkit
  v0.0.14, up from v0.0.13. A redacted session now hashes
  independently: a redactor gives each redacted response the
  `request_hash` of the request the redacted session rebuilds for it,
  so readers verify it with no exception for redacted entries. The
  inspect pane's verify tally, which has none, no longer reports a
  redacted session made under that rule as FAILED, which answers the
  report in #38.
- Added: the session pane of a redacted session says so (#38): "redacted
  from PSEUDONYM (profile P)", with its counts of redacted and native
  entries. Since format 0.19 `redacted_from.session` is the original's
  keyed pseudonym, so the pane never offers it as a session to open. A
  session whose ID is not the one its `redacted_from` derives
  (`agentsession.CheckRedactedID`) is noted, as `agentsession verify`
  notes it, and does not fail. An entry's pane gives the original
  entries a redacted entry was made from ("made from original entries
  3-7"), or says the redaction added it.

## v0.0.14 - 2026-10-18

- Dependencies: agentsession v0.0.27 (format 0.13), agentturn and
  agentturn/session v0.0.30, agenttool v0.0.23, agentpolicy v0.0.17,
  agentkit v0.0.13, openresponses v0.0.15.
- Fixed: agentturn v0.0.29 → v0.0.30 clears an entry's `redacted`
  member when the recorder copies an entry it read (a callee link copied
  onto another branch, the env a child is written with), so a redacted
  session the binary resumes takes its run as native entries.
- Changed: with agentsession v0.0.27 the binary raises every session it
  appends to to format 0.13, and so does an embedder that takes this
  release's floor by version selection (dax). A reader built on
  agentsession before v0.0.27 refuses those files until it is upgraded.

## v0.0.13 - 2026-10-13

- Dependencies: agentsession v0.0.24 (format 0.12), agentturn and
  agentturn/session v0.0.28.
- Changed: with agentsession v0.0.24 the binary raises every session it
  appends to to format 0.12, and so does an embedder that takes this
  release's floor by version selection (dax). A reader built on
  agentsession before v0.0.24 refuses those files until it is upgraded.
- Added: `view.Link` names a callee in another system (`URL`, `Task`,
  `Context`) on a subsession or peer link, and `CancelSent` when a
  cancel notice names the link. A cancel notice is folded into the link
  it names and never listed as a link of its own. As in agentsession's
  `Callees`, it marks only a link that landed before it.
- Changed: the tree labels a peer as a peer and a subsession in another
  system apart from one of this store, with its URL, task, call and
  session, and any subsession or peer with "cancel sent" when one was
  sent. A link that names no session cannot be opened: selecting it
  says so and stays on the tree. A real A2A run shows its callees once
  agentturn's recorder writes them as links (agentturn#252, not yet
  released).
- Added: a call's detail pane lists its callees, with the dispatch each
  belongs to and whether a cancel was sent or the callee answered the
  call.

## v0.0.12 - 2026-10-11

- Dependencies: agentturn and agentturn/session v0.0.27, agentpolicy
  v0.0.16, agentkit v0.0.12.
- Changed: `client.Direct` sends a `Reply` with the Controller's
  context, as agentturn's `Control.Reply` now takes one. The view holds
  no identity: an embedder that knows who answered wraps the Controller
  and adds `agentturn.ContextWithAnswerer` to a `Reply`'s context, and
  the record names them; without it the answer is unattributed.
- Changed: the binary and the tests build their kits with
  `agentkit.WithQuestionEvents()`. Since agentkit v0.0.12 a kit puts a
  tool's question as an event only when asked to; without it the
  question goes unasked.
- Changed: a note given with an accept of a question about a call
  reaches the model as the person's own message after the batch's
  outputs (agentturn v0.0.24), no longer appended to the decision's
  note the call reads.

## v0.0.11 - 2026-10-10

- Changed: agentconsole is the view and is not the controller (#33,
  #34). It takes the session through a `client.Record` and agentturn's
  events through a `client.Source` (`Subscribe` and `State`, which an
  `agentturn.Control` has), and sends what the user asks as intents to a
  `client.Controller`: `console.Run(ctx, record, source, controller,
  ...)`. The TUI keeps no runs of its own and nothing blocks on one:
  whether a run goes, and a run's error, are the view's, from the
  events. The view reads agentturn's events (`view.View.Live` takes an
  `agentturn.Event`), copied by `client.Copy` and queued by
  `client.Live`, so it sees what they carry: whether a pending call was
  only held, the part of a call a question is about, whether a held call
  was dispatched before, what stopped a run (`view.Turn.Cause`) and what
  failed it (`view.Turn.Err`).
- Added: the intents, plain values in `client`: `Prompt`, `Queue`
  (steer or follow-up), `Abort`, `Answer` (the permission panel's),
  `Reply` (a question's, the note included) and `ContinueFrom`. A
  `client.Controller` returns promptly, nil or a refusal the client
  shows. `client.Direct(control)` maps them onto an `agentturn.Control`:
  a prompt or an answer returns once its run has started, or with the
  error that kept it from starting; `ContinueFrom` goes to a control
  with a `ContinueFrom` method (agentkit's) and is refused with
  `client.ErrNoHeadMove` otherwise. A product writes its own controller
  for intents it handles itself.
- Added: `client.RecordOf`, a `Record` over a `session.Recorder`'s
  store.
- Added: the permission panel shows the part of a compound call the
  policy asked about (`view.Permission.Subject`) and says when a call
  was started before it was held and may have run
  (`view.Permission.MayHaveRun`). The status line says what stopped
  the last run (`view.Turn.Cause`).
- Added: a question's answer carries the user's words: `a` allows with
  a note, and a refusal's reason is the answer's `Note`
  (`agenttool.Answer`). A question with a form to fill can only be
  refused; a URL question shows its URL.
- Added: `console.Submit.FollowUp` queues a line for when the run would
  stop (`agentturn.QueueFollowUp`). A submission with more than one of
  a prompt, a steer and a follow-up is refused.
- Fixed: a call the policy only held beside another call's question is
  no longer asked about while the run's end is in hand; the answers to
  the batch release it (the first part of #9). After a restart every
  pending call is still asked about, since the record cannot tell them
  apart.
- Removed: `client/native` and `client/kitbackend`, and `client.Backend`,
  `client.Control`, `client.LiveEvent` and its event types,
  `client.FromEvent`, `client.Question` and `client.Reply`. What the
  backends did around a run is agentkit's `Kit.Control`, and a
  question is agentturn's `Question` event answered with `Reply`.
- Changed: `cmd/agentconsole` assembles its agent with agentkit and
  drives it through `Kit.Control`.
- Changed: requires agentturn v0.0.22, agentkit v0.0.10, agentpolicy
  v0.0.15 and agenttool v0.0.22.

## v0.0.10 - 2026-10-10

- Added: `console.WithSubmit`, which hands each line the user submits to
  the program running the client, which decides what it is: a prompt
  (`Submit.Prompt`), a steer (`Submit.Steer`) or neither, with a reply
  shown over the input (`Submit.Reply`). `console.Line` carries the text
  and whether a run was going. The function runs off the program's
  goroutine, one line at a time; a line submitted meanwhile is shown
  waiting, and an error is shown with the line given back to the input.
  Without the option a line is a prompt or a steer, as before. The
  client gives a slash no meaning of its own: a product's commands are
  the product's.

## v0.0.9 - 2026-10-06

- Changed: the model's messages are labelled "agent", not "assistant",
  in the conversation, in the tree's branch labels (`view.Branch.Label`)
  and in the detail pane's title, and the user's messages in the
  conversation "user", not "you", as the tree and the pane name them. `view.Branch.Role` still holds the
  record's role.

## v0.0.8 - 2026-10-06

- Added: per-tool rendering of calls. `console.WithToolRenderers` takes
  a `toolview.Renderer` by tool name (new package `toolview`); a
  renderer gives the head after the call's name and the body under its
  line, as lines with roles the client styles, and may decline either,
  which shows the call as before. It reads only `view.Call`, so it draws
  a call streaming, under a permission, running and from a session's
  history alike, and needs nothing from the tool: an MCP tool's calls
  can be drawn too. Expanded (ctrl+o) the raw arguments are still shown
  above the body, and the permission panel still shows them as given.
- Added: `view.Call.Schema`, the parameters schema of the call's tool as
  the config in force at the call holds it, so a renderer can tell the
  tool it was written for from another of the same name.
- Changed: the spinner turns in the dot of the run line and of each call
  in motion, in place of ●, instead of at the right edge, where it was
  far from the eye. A call's dot goes back to ○ when it ends, and the
  run line's when the run does.
- Changed: while a run goes, the run line says what it is doing, after
  the turn's figures, in place of "running": "waiting" on the model,
  "thinking", "writing", "calling read" while a call's arguments stream
  or it waits to run, "running read" (or "running 3 tools") while tools
  run, and "retrying" after a failed model call.

- Added: a steer typed while a run goes is listed over the input,
  "queued: …", from the moment it is sent until the run takes it into
  the conversation, and an input left for the next run as "queued for
  the next run: …". The list is the record's queued entries
  (`view.Model.Queued`, new type `view.Queued`); the native backend's
  steer now writes its queued entry before the agent takes it, so it
  shows at once even while a tool runs quietly. A steer that cannot be
  written says so and gives its text back to the input.
- Changed (breaking): `client.Control.Steer` is
  `Steer(ctx, items...) error`. The context carries a trigger for the
  record; the error says the input was not queued.
- Changed: `native.New` (and so `kitbackend.New`) hands the agent the
  inputs a resumed session, or a fork's base, still owes: a steer or
  follow-up queued before an abort and a quit is listed as queued and
  taken by the next run, after its prompt, rather than dropped.
- Added: Shift-Enter puts a new line in the input, where Enter sends it;
  in a refusal's reason too. A terminal reports Shift-Enter apart from
  Enter only over the kitty keyboard protocol, so Alt-Enter and Ctrl-J
  do the same for the rest. The keys screen lists them.
- Fixed: an input ending in a line break (a paste that ends in one, now
  a typed one too) was sized a row short, and scrolled its first line
  out of view.

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
