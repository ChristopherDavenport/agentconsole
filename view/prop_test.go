package view

// A randomized interleaving test. A script is played once as the agent and
// its recorder would (prop_sim_test.go) to give one reference timeline, and
// the two streams a view consumes, the record's changes and the live
// events, are delivered to the view in a random interleaving that keeps
// each stream's own order. A snapshot starts it at a random prefix of the
// log, and resets and head moves are injected at random points. After every
// step the invariants hold; once both streams are in, the view equals a
// rendering of the final record alone.
//
//	AGENTCONSOLE_SEEDS=20000 go test ./view -run TestInterleavings
//	AGENTCONSOLE_SEED0=1234 AGENTCONSOLE_SEEDS=1 go test ./view -run TestInterleavings -v
//
// replays a seed. The default is 150 seeds.

import (
	"fmt"
	"math/rand"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/client"
)

func envInt(name string, def int) int {
	if s := os.Getenv(name); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
	}
	return def
}

// profile skews the generator towards one kind of trouble, so each gets
// exercised often; seed mod 4 picks it.
type profile struct{ rewind, modelSwitch, calls, deferred, reuse int }

var profiles = []profile{
	{18, 25, 40, 30, 3}, // balanced
	{45, 50, 55, 20, 3}, // heads and model switches
	{20, 20, 70, 60, 3}, // calls, deferred and aborted
	{20, 25, 40, 30, 2}, // reused IDs
}

func genScript(r *rand.Rand, pf profile) pScript {
	var sc pScript
	sc.LateCfg = r.Intn(100) < 40
	models := []string{"m1", "m2", "m3"}
	n := 1 + r.Intn(4)
	for i := 0; i < n; i++ {
		if i > 0 && r.Intn(100) < pf.rewind {
			kind := "rewind"
			if r.Intn(100) < 40 {
				kind = "headrec"
			}
			sc.Elems = append(sc.Elems, pElem{Kind: kind, To: r.Intn(50)})
			continue
		}
		if i > 0 && r.Intn(100) < 8 {
			sc.Elems = append(sc.Elems, pElem{Kind: "bookmark", To: r.Intn(50)})
			continue
		}
		if i > 0 && r.Intn(100) < 8 {
			kind := "link"
			if r.Intn(2) == 0 {
				kind = "compact"
			}
			sc.Elems = append(sc.Elems, pElem{Kind: kind, To: r.Intn(50)})
			continue
		}
		el := pElem{Kind: "run", Approve: r.Intn(2) == 0, HiddenPrompt: r.Intn(100) < 10}
		if i == 0 || r.Intn(100) < 25 {
			el.Model = models[r.Intn(len(models))]
		}
		for t := 0; t < 1+r.Intn(3); t++ {
			turn := pTurn{Named: r.Intn(2) == 0}
			if !turn.Named {
				turn.NameAfter = r.Intn(3)
			}
			if r.Intn(100) < pf.modelSwitch {
				turn.Model = models[r.Intn(len(models))]
			}
			for k := 0; k < 1+r.Intn(2); k++ {
				it := pItem{Reuse: r.Intn(pf.reuse), Reason: r.Intn(4) == 0, Anon: r.Intn(10) == 0}
				it.Marked = it.Reason && r.Intn(100) < 40
				turn.Items = append(turn.Items, it)
			}
			if r.Intn(100) < pf.calls {
				for k := 0; k < 1+r.Intn(2); k++ {
					c := pCall{Defer: r.Intn(100) < pf.deferred, Hang: r.Intn(100) < 12, Progress: r.Intn(100) < 35}
					if !c.Defer {
						c.Children = r.Intn(3) / 2
					}
					turn.Calls = append(turn.Calls, c)
				}
				turn.Steer = r.Intn(100) < 25
			}
			switch x := r.Intn(100); {
			case x < 8:
				turn.Retries = 1 + r.Intn(2)
			case x < 14:
				turn.Withheld = true
			case x < 20:
				turn.Cut = true
			case x < 25:
				turn.Failed = true
			}
			el.Turns = append(el.Turns, turn)
		}
		sc.Elems = append(sc.Elems, el)
	}
	return sc
}

// opTrace records what was delivered, for the failure report.
type opTrace []string

func (t *opTrace) add(f string, a ...any) { *t = append(*t, fmt.Sprintf(f, a...)) }

// copyEntry gives a follower its own copy of an entry, as a store does.
func copyEntry(e agentsession.Entry) (agentsession.Entry, error) {
	b, err := agentsession.MarshalEntry(e)
	if err != nil {
		return nil, err
	}
	return agentsession.UnmarshalEntry(b)
}

type follower struct {
	tl      *timeline
	fs      *agentsession.Session
	entries int // how many entries the session holds
}

// build reads a session holding the first k things the store yielded.
func (f *follower) build(k int) error {
	fs := agentsession.New(f.tl.header)
	for i := 0; i < k; i++ {
		op := f.tl.ops[i]
		if op.isHead {
			if err := fs.Branch(op.target); err != nil {
				return err
			}
			continue
		}
		c, err := copyEntry(f.tl.entries[op.entry])
		if err != nil {
			return err
		}
		if _, err := fs.Extend(c); err != nil {
			return fmt.Errorf("extend %d: %w", i, err)
		}
	}
	f.fs, f.entries = fs, k
	return nil
}

// next yields the changes for the next thing the store holds.
func (f *follower) next() ([]agentsession.Change, error) {
	op := f.tl.ops[f.entries]
	f.entries++
	if op.isHead {
		if err := f.fs.Branch(op.target); err != nil {
			return nil, err
		}
		return []agentsession.Change{{Kind: agentsession.Head, Session: f.fs, Leaf: op.target}}, nil
	}
	c, err := copyEntry(f.tl.entries[op.entry])
	if err != nil {
		return nil, err
	}
	before := f.fs.Leaf()
	r, err := f.fs.Extend(c)
	if err != nil {
		return nil, fmt.Errorf("extend %d: %w", op.entry, err)
	}
	out := []agentsession.Change{{Kind: agentsession.Appended, Session: f.fs, ID: r.ID, Entry: c}}
	if l := f.fs.Leaf(); l != r.ID && l != before {
		out = append(out, agentsession.Change{Kind: agentsession.Head, Session: f.fs, Leaf: l})
	}
	return out, nil
}

type rowInfo struct {
	live bool
}

// deliver plays a timeline into a view in the interleaving the seed picks,
// checking the invariants after every step. It returns the failure, if any,
// with the trace that led to it.
func deliver(tl *timeline, seed int64) (string, opTrace) {
	r := rand.New(rand.NewSource(seed))
	var trace opTrace
	v := New()
	f := &follower{tl: tl}
	n, m := len(tl.ops), len(tl.lives)

	k0 := r.Intn(n + 1)
	if err := f.build(k0); err != nil {
		return "setup: " + err.Error(), trace
	}
	trace.add("snapshot at %d of %d entries", k0, n)
	v.Record(agentsession.Change{Kind: agentsession.Snapshot, Session: f.fs})
	c := &checker{tl: tl, v: v, f: f, prev: map[string]rowInfo{}, openEph: map[string]bool{}, gone: map[string]bool{},
		items: map[string]*itemTruth{}, completed: map[string]bool{}, calls: map[string]*callTruth{}, everLanded: map[string]bool{}, everOut: map[string]bool{},
		flushed: map[string]bool{}, liveEndedRuns: map[string]bool{}}
	if msg := c.step("snapshot", true); msg != "" {
		return msg, trace
	}
	li, resets := 0, 0
	for f.entries < n || li < m {
		canRec, canLive := f.entries < n, li < m
		switch x := r.Intn(100); {
		case x < 4 && resets < 3 && (canRec || canLive):
			k := r.Intn(n + 1)
			if err := f.build(k); err != nil {
				return "setup: " + err.Error(), trace
			}
			resets++
			trace.add("reset to %d entries", k)
			v.Record(agentsession.Change{Kind: agentsession.Reset, Session: f.fs})
			if msg := c.step("reset", true); msg != "" {
				return msg, trace
			}
		case canLive && (!canRec || r.Intn(2) == 0):
			ev := tl.lives[li].ev
			trace.add("live %d %T", li, ev)
			li++
			c.liveDelivered(ev)
			v.Live(ev)
			if msg := c.step(fmt.Sprintf("live %T", ev), false); msg != "" {
				return msg, trace
			}
		default:
			if op := tl.ops[f.entries]; op.isHead {
				trace.add("record head to %.12s", op.target)
			} else {
				trace.add("record entry %d %s", op.entry, tl.entries[op.entry].EntryType())
			}
			changes, err := f.next()
			if err != nil {
				return "setup: " + err.Error(), trace
			}
			for _, ch := range changes {
				v.Record(ch)
				trace.add("  change %v", ch.Kind)
				if msg := c.step("record "+ch.Kind.String(), ch.Kind == agentsession.Head); msg != "" {
					return msg, trace
				}
			}
		}
	}
	if msg := c.converged(); msg != "" {
		return "converge: " + msg, trace
	}
	// Reading the whole record in again must give the same view.
	if err := f.build(n); err != nil {
		return "setup: " + err.Error(), trace
	}
	trace.add("final reset to %d entries", n)
	v.Record(agentsession.Change{Kind: agentsession.Reset, Session: f.fs})
	if msg := c.step("final reset", true); msg != "" {
		return msg, trace
	}
	if msg := c.converged(); msg != "" {
		return "converge after a final reset: " + msg, trace
	}
	return "", trace
}

type checker struct {
	tl       *timeline
	v        *View
	f        *follower
	prev     map[string]rowInfo
	running  bool
	prevLf   string
	prevTail string
	lastEnd  *client.RunEnded
	openEph  map[string]bool
	gone     map[string]bool
	// standing is the run whose live end was delivered before the
	// record's end entry for it: its facts stand until that entry lands.
	// An end entry that landed first, and was taken back by a reset
	// later, leaves nothing standing.
	standing  string
	justEnded bool

	// What the live stream has said, as delivered.
	items         map[string]*itemTruth
	completed     map[string]bool
	calls         map[string]*callTruth
	everLanded    map[string]bool // items an entry has been delivered for, ever
	everOut       map[string]bool // calls an output has been delivered for, ever
	flushed       map[string]bool // runs settled by both halves of their end
	liveEndedRuns map[string]bool
	turnNo        int
	turnModel     string
	attempt       int
}

type itemTruth struct{ run string }

type callTruth struct {
	run                  string
	parent               string
	children             []string
	dispatched, finished bool
	deferred, failed     bool
	partial, result      string
}

func (c *checker) liveDelivered(ev client.LiveEvent) {
	eph := func(item openresponses.Item) {
		if u := rowUID(Row{Item: item}); c.tl.ephemeral[u] {
			c.openEph[u] = true
		}
	}
	item := func(it openresponses.Item, completed bool) {
		eph(it)
		if _, isOut := it.(*openresponses.FunctionCallOutput); isOut {
			return
		}
		if client.ItemID(it) == "" && client.CallID(it) == "" {
			return // an item with no ID is not overlaid
		}
		u := rowUID(Row{Item: it})
		c.items[u] = &itemTruth{run: ev.Run()}
		if completed {
			c.completed[u] = true
		}
	}
	call := func(id, run string) *callTruth {
		t := c.calls[id]
		if t == nil {
			t = &callTruth{run: run}
			c.calls[id] = t
		}
		return t
	}
	switch e := ev.(type) {
	case *client.RunStarted:
		c.running = true
		c.turnNo, c.turnModel, c.attempt = 0, "", 0
		clear(c.openEph) // a cut stream's leftovers go with their run, not a later response
	case *client.TurnStarted:
		c.turnNo, c.turnModel, c.attempt = e.Turn, e.Model, 0
	case *client.RunEnded:
		c.running = false
		c.lastEnd = e
		c.justEnded = true
		c.liveEndedRuns[e.RunID] = true
		c.turnNo, c.attempt = 0, 0
		clear(c.openEph)
	case *client.ItemOpened:
		item(e.Item, false)
	case *client.ItemUpdated:
		item(e.Item, false)
	case *client.ItemCompleted:
		item(e.Item, true)
	case *client.ModelRetrying:
		c.attempt = e.Attempt
		for u := range c.openEph {
			c.gone[u] = true
		}
		clear(c.openEph)
	case *client.ResponseCompleted:
		// What the turn streamed and never completed has no entry
		// coming: it must be gone from here on, not at the run's end.
		for u := range c.openEph {
			c.gone[u] = true
		}
		clear(c.openEph)
	case *client.ToolOpened:
		t := call(e.CallID, e.RunID)
		t.parent = e.Parent
		if e.Parent != "" {
			p := call(e.Parent, e.RunID)
			p.children = append(p.children, e.CallID)
		}
	case *client.ToolDispatched:
		call(e.CallID, e.RunID).dispatched = true
	case *client.ToolProgress:
		call(e.CallID, e.RunID).partial = e.Partial
	case *client.ToolFinished:
		t := call(e.CallID, e.RunID)
		t.finished, t.deferred, t.failed, t.result = true, e.Deferred, e.Err != nil, e.Result
		if e.Err != nil && e.Result == "" {
			t.result = e.Err.Error()
		}
	}
}

// rowUID names a row by the unique text the generator gave its item.
func rowUID(row Row) string {
	switch it := row.Item.(type) {
	case *openresponses.FunctionCall:
		return "call:" + it.CallID
	case *openresponses.FunctionCallOutput:
		return "out:" + it.CallID
	case *openresponses.Message:
		return firstToken(it.Text())
	case *openresponses.ReasoningItem:
		return firstToken(it.Summary.Text())
	}
	return fmt.Sprintf("?%T", row.Item)
}

// step checks the invariants. moved says the viewed path may legitimately
// have changed under the rows (a head move, a reset, a snapshot).
func (c *checker) step(what string, moved bool) string {
	m := c.v.Model()
	fail := func(f string, a ...any) string {
		return fmt.Sprintf("after %s: %s\n  model: %s\n  view: %s", what, fmt.Sprintf(f, a...), describeModel(m), dump(c.v))
	}
	// A reference rendering of the record as delivered.
	ref := render(c.f.fs)
	if c.lastEnd != nil && c.justEnded && !ref.endRuns[c.lastEnd.RunID] {
		c.standing = c.lastEnd.RunID
	}
	c.justEnded = false
	if c.standing != "" && ref.endRuns[c.standing] {
		c.standing = ""
	}
	if ref.err != "" {
		return fail("reference: %s", ref.err)
	}
	for run := range c.liveEndedRuns {
		if ref.endRuns[run] {
			c.flushed[run] = true
		}
	}
	for u := range ref.uids {
		c.everLanded[u] = true
	}
	for id := range ref.landedOuts {
		c.everOut[id] = true
	}
	cur := map[string]rowInfo{}
	var committed []string
	for _, row := range m.Rows {
		u := rowUID(row)
		if _, dup := cur[u]; dup {
			return fail("row %s is rendered twice", u)
		}
		cur[u] = rowInfo{live: row.Live}
		if !row.Live {
			committed = append(committed, u)
		}
		if c.tl.ephemeral[u] && !row.Live {
			return fail("row %s is committed but no entry holds it", u)
		}
	}
	// The committed rows, the model in force and the branch tips are the
	// record's, with no lag: they equal the reference at every step.
	if strings.Join(committed, ",") != strings.Join(ref.rows, ",") {
		return fail("committed rows %v, the record's path has %v", committed, ref.rows)
	}
	if m.Config != ref.config {
		return fail("config %q, the record's line has %q", m.Config, ref.config)
	}
	if strings.Join(m.Leaves, ",") != strings.Join(ref.leaves, ",") {
		return fail("leaves %v, want %v", m.Leaves, ref.leaves)
	}
	if m.Leaf != ref.leaf {
		return fail("leaf %s, want %s", m.Leaf, ref.leaf)
	}
	if msg := c.treeFacts(m, ref); msg != "" {
		return fail("%s", msg)
	}
	// A committed row is not dropped unless the viewed path moved; a live
	// row is governed by the liveness rules below.
	for u, info := range c.prev {
		if _, ok := cur[u]; ok || c.tl.ephemeral[u] || info.live {
			continue
		}
		if !moved && !c.leftThePath(ref) {
			return fail("row %s (committed) was rendered and then dropped", u)
		}
	}
	c.prevTail = ref.tail
	c.prev = cur
	c.prevLf = m.Leaf
	for u := range c.gone {
		if _, ok := cur[u]; ok {
			return fail("row %s was dropped by its response's end or a retry and is back", u)
		}
	}
	if msg := c.liveness(m, ref, cur); msg != "" {
		return fail("%s", msg)
	}
	if msg := c.callRows(m, ref); msg != "" {
		return fail("%s", msg)
	}
	if m.Turn.State == Running {
		if m.Turn.Number != c.turnNo || m.Turn.Attempt != c.attempt || (c.turnNo > 0 && m.Turn.Model != c.turnModel) {
			return fail("turn number %d model %q attempt %d, the stream is at %d %q %d", m.Turn.Number, m.Turn.Model, m.Turn.Attempt, c.turnNo, c.turnModel, c.attempt)
		}
	} else if m.Turn.Number != 0 || m.Turn.Attempt != 0 {
		return fail("turn number %d attempt %d with no run going", m.Turn.Number, m.Turn.Attempt)
	}
	// What waits. The record decides, except while the live stream is
	// ahead of it: a run end the record has not delivered stands.
	if !c.running {
		wantPerms, wantCuts := ref.perms, ref.cuts
		withQuestions := true
		if f := c.lastEnd; f != nil && c.standing == f.RunID && (ref.lastRun == f.RunID || !ref.allRuns[f.RunID]) {
			wantPerms, wantCuts, withQuestions = nil, nil, false
			for _, p := range f.Pending {
				if f.Reason == agentturn.ReasonInputRequired && p.Reason == agentturn.PendingDeferred {
					wantPerms = append(wantPerms, p.CallID)
				} else {
					wantCuts = append(wantCuts, p.CallID)
				}
			}
			sort.Strings(wantPerms)
			sort.Strings(wantCuts)
		}
		var gotPerms, gotCuts []string
		for _, p := range m.Permissions {
			if withQuestions {
				gotPerms = append(gotPerms, p.CallID+"|"+p.Question)
			} else {
				gotPerms = append(gotPerms, p.CallID)
			}
		}
		for _, p := range m.CutOff {
			gotCuts = append(gotCuts, p.CallID)
		}
		sort.Strings(gotPerms)
		sort.Strings(gotCuts)
		if strings.Join(gotPerms, ",") != strings.Join(wantPerms, ",") || strings.Join(gotCuts, ",") != strings.Join(wantCuts, ",") {
			return fail("permissions %v cut off %v, want %v and %v", gotPerms, gotCuts, wantPerms, wantCuts)
		}
	}
	// The turn.
	if (m.Turn.State == Running) != c.running {
		return fail("turn %v with a live run open=%v", m.Turn.State, c.running)
	}
	// What waits to join the conversation is never a row as well.
	for _, q := range m.Queued {
		t := textOfItem(q.Item)
		for _, row := range m.Rows {
			if textOfItem(row.Item) == t {
				return fail("input %q is queued and a row", t)
			}
		}
	}
	if m.Turn.State == RequiresAction && len(m.Permissions) == 0 {
		return fail("requires_action with no permission")
	}
	if !c.running && len(m.Permissions) > 0 && m.Turn.State != RequiresAction {
		return fail("permissions %v with the turn %v", m.Permissions, m.Turn.State)
	}
	for _, p := range m.Permissions {
		if !c.tl.deferred[p.CallID] {
			return fail("permission %s was never deferred", p.CallID)
		}
	}
	return ""
}

func (c *checker) leftThePath(ref rendering) bool {
	for _, id := range ref.pathIDs {
		if id == c.prevTail {
			return false
		}
	}
	return c.prevTail != ""
}

// converged checks the view against the final record alone.
func (c *checker) converged() string {
	m := c.v.Model()
	ref := render(c.f.fs)
	fail := func(f string, a ...any) string {
		return fmt.Sprintf(f, a...) + "\n  model: " + describeModel(m)
	}
	if c.running {
		return fail("a run is still open")
	}
	var all []string
	for _, row := range m.Rows {
		if row.Live {
			return fail("live row %s left after both streams ended", rowUID(row))
		}
		all = append(all, rowUID(row))
	}
	if strings.Join(all, ",") != strings.Join(ref.rows, ",") {
		return fail("rows %v, want %v", all, ref.rows)
	}
	var perms, cuts []string
	for _, p := range m.Permissions {
		perms = append(perms, p.CallID+"|"+p.Question)
	}
	for _, p := range m.CutOff {
		cuts = append(cuts, p.CallID)
	}
	sort.Strings(perms)
	sort.Strings(cuts)
	if strings.Join(perms, ",") != strings.Join(ref.perms, ",") {
		return fail("permissions %v, want %v", perms, ref.perms)
	}
	if strings.Join(cuts, ",") != strings.Join(ref.cuts, ",") {
		return fail("cut off %v, want %v", cuts, ref.cuts)
	}
	wantState := Idle
	if len(ref.perms) > 0 {
		wantState = RequiresAction
	}
	if m.Turn.State != wantState {
		return fail("turn %v, want %v", m.Turn.State, wantState)
	}
	if len(c.v.items) != 0 {
		return fail("%d overlay items left", len(c.v.items))
	}
	if len(c.v.liveEnded) != 0 {
		return fail("%d live run ends left unsettled", len(c.v.liveEnded))
	}
	// Every steer the generator plays is taken by its run.
	if len(m.Queued) != 0 {
		return fail("inputs %v left queued", m.Queued)
	}
	return ""
}

func describeModel(m Model) string {
	var b strings.Builder
	for _, row := range m.Rows {
		l := ""
		if row.Live {
			l = "~"
		}
		fmt.Fprintf(&b, "[%s%s]", l, rowUID(row))
	}
	fmt.Fprintf(&b, " turn=%v config=%q perms=%d cut=%d", m.Turn.State, m.Config, len(m.Permissions), len(m.CutOff))
	return b.String()
}

// The reference renderer. It reads the record alone and shares nothing with
// view.go: a session in, what a client would render of its committed part
// out.
type rendering struct {
	err     string
	rows    []string
	config  string
	leaf    string
	leaves  []string
	perms   []string
	cuts    []string
	pathIDs []string
	uids    map[string]bool
	// Runs: every run entry's ID, those with an end entry, and the last
	// run of the viewed line.
	allRuns, endRuns map[string]bool
	lastRun          string
	// tail is the end of the viewed line, parent the entries' parents, and
	// outputs the text of each call's output on the viewed line; landedOuts
	// the calls with an output anywhere.
	tail       string
	parent     map[string]string
	outputs    map[string]string
	landedOuts map[string]bool
	dispatched map[string]bool
	entryIDs   map[string]bool
	// What a tree shows, computed on its own: the branches (tips that no
	// other tip's line passes through, and the viewed one), the links, the
	// compactions on the viewed line, and how many entries there are.
	branches []refBranch
	links    []refLink
	folds    []refFold
	n        int
}

type refBranch struct {
	leaf, role string
	current    bool
}

type refLink struct{ rel, session, callID, entry string }

type refFold struct {
	uid, entry, firstKept string
	summaryLen, pinned    int
}

func textOfItem(item openresponses.Item) string {
	if m, ok := item.(*openresponses.Message); ok {
		return m.Text()
	}
	return ""
}

func render(s *agentsession.Session) rendering {
	var out rendering
	entries := s.Entries()
	byID := map[string]agentsession.Entry{}
	kids := map[string][]string{}
	out.uids = map[string]bool{}
	out.allRuns, out.endRuns = map[string]bool{}, map[string]bool{}
	out.parent, out.outputs, out.landedOuts = map[string]string{}, map[string]string{}, map[string]bool{}
	out.dispatched, out.entryIDs = map[string]bool{}, map[string]bool{}
	for _, e := range entries {
		if r, ok := e.(*agentsession.RunEntry); ok {
			out.allRuns[r.RunID] = true
			if r.IsEnd() {
				out.endRuns[r.RunID] = true
			}
		}
		b := e.Base()
		byID[b.ID] = e
		out.parent[b.ID] = b.Parent
		out.entryIDs[b.ID] = true
		if c, ok := e.(*agentsession.CustomEntry); ok {
			if item, _, ok := session.MarkedItem(c); ok {
				out.uids[rowUID(Row{Item: item})] = true
			}
		}
		if it, ok := e.(*agentsession.ItemEntry); ok {
			if o, ok := it.Item.(*openresponses.FunctionCallOutput); ok {
				out.landedOuts[o.CallID] = true
			}
		}
		kids[b.Parent] = append(kids[b.Parent], b.ID)
		if l, ok := e.(*agentsession.LinkEntry); ok {
			out.links = append(out.links, refLink{rel: l.Rel, session: l.Session, callID: l.CallID, entry: l.ID})
		}
		if c, ok := e.(*agentsession.CompactionEntry); ok {
			out.uids[rowUID(Row{Item: c.Summary})] = true
		}
		if it, ok := e.(*agentsession.ItemEntry); ok {
			out.uids[rowUID(Row{Item: it.Item})] = true
		}
	}
	pathTo := func(id string) []agentsession.Entry {
		var rev []agentsession.Entry
		for id != "" {
			e, ok := byID[id]
			if !ok {
				break
			}
			rev = append(rev, e)
			id = e.Base().Parent
		}
		for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
			rev[i], rev[j] = rev[j], rev[i]
		}
		return rev
	}
	isItem := func(e agentsession.Entry) bool { _, ok := e.(*agentsession.ItemEntry); return ok }
	// The item a branch rests on: bookkeeping stands for its parent.
	rest := func(id string) string {
		for id != "" {
			e, ok := byID[id]
			if !ok || isItem(e) || e.Base().Parent == "" {
				return id
			}
			id = e.Base().Parent
		}
		return id
	}
	leaf := s.Leaf()
	out.leaf = rest(leaf)
	// The line behind the leaf: what the producing run wrote after the
	// item, up to what a later run or turn wrote.
	tail := leaf
	for {
		next := ""
		for _, k := range kids[tail] {
			switch x := byID[k].(type) {
			case *agentsession.ItemEntry, *agentsession.ConfigEntry:
				continue
			case *agentsession.RunEntry:
				if x.IsStart() {
					continue
				}
			}
			next = k
		}
		if next == "" {
			break
		}
		tail = next
	}
	line := pathTo(tail)
	for _, e := range line {
		out.pathIDs = append(out.pathIDs, e.Base().ID)
		if it, ok := e.(*agentsession.ItemEntry); ok && it.IsVisible() {
			out.rows = append(out.rows, rowUID(Row{Item: it.Item}))
		}
		if c, ok := e.(*agentsession.CustomEntry); ok {
			if item, _, ok := session.MarkedItem(c); ok {
				out.rows = append(out.rows, rowUID(Row{Item: item}))
			}
		}
		if c, ok := e.(*agentsession.CompactionEntry); ok {
			out.rows = append(out.rows, rowUID(Row{Item: c.Summary}))
			out.folds = append(out.folds, refFold{uid: rowUID(Row{Item: c.Summary}), entry: c.ID, firstKept: c.FirstKept, summaryLen: len([]rune(textOfItem(c.Summary))), pinned: len(c.Pinned)})
		}
	}
	out.tail = tail
	for _, e := range line {
		switch x := e.(type) {
		case *agentsession.ItemEntry:
			if o, ok := x.Item.(*openresponses.FunctionCallOutput); ok {
				out.outputs[o.CallID] = o.Output.String()
			}
		case *agentsession.DispatchEntry:
			out.dispatched[x.CallID] = true
		}
	}
	for _, e := range line {
		if c, ok := e.(*agentsession.ConfigEntry); ok && c.Model != "" {
			out.config = c.Model
		}
	}
	// Branch tips.
	seen := map[string]bool{}
	for _, e := range entries {
		id := e.Base().ID
		if len(kids[id]) > 0 {
			continue
		}
		id = rest(id)
		if !seen[id] {
			seen[id] = true
			out.leaves = append(out.leaves, id)
		}
	}
	out.n = len(entries)
	// Branches: a tip is a branch unless another tip's line passes through
	// it; the viewed line is one whatever. Newest first.
	through := map[string]bool{}
	for _, tip := range out.leaves {
		for _, e := range pathTo(tip) {
			if e.Base().ID != tip {
				through[e.Base().ID] = true
			}
		}
	}
	var tips []string
	for _, tip := range out.leaves {
		if !through[tip] {
			tips = append(tips, tip)
		}
	}
	if out.leaf != "" && !slices.Contains(tips, out.leaf) {
		tips = append(tips, out.leaf)
	}
	for i := len(tips) - 1; i >= 0; i-- {
		b := refBranch{leaf: tips[i], current: tips[i] == out.leaf}
		for id, hops := tips[i], 0; id != "" && hops < 64; hops++ {
			e, ok := byID[id]
			if !ok {
				break
			}
			if it, ok := e.(*agentsession.ItemEntry); ok && it.IsVisible() {
				if m, ok := it.Item.(*openresponses.Message); ok {
					b.role = string(m.Role)
					break
				}
			}
			id = e.Base().Parent
		}
		out.branches = append(out.branches, b)
	}
	// What waits, from the line's last run.
	var last *agentsession.RunEntry
	for _, e := range line {
		if r, ok := e.(*agentsession.RunEntry); ok {
			last = r
		}
	}
	if last != nil {
		out.lastRun = last.RunID
	}
	if last != nil && last.IsEnd() {
		outputs := map[string]bool{}
		holds := map[string]string{} // call ID to the question while the latest decision is a hold
		for _, e := range line {
			switch x := e.(type) {
			case *agentsession.ItemEntry:
				if o, ok := x.Item.(*openresponses.FunctionCallOutput); ok {
					outputs[o.CallID] = true
				}
			case *agentsession.DecisionEntry:
				if x.Verdict == agentsession.VerdictHold {
					holds[x.CallID] = x.Reason
				} else {
					delete(holds, x.CallID)
				}
			case *agentsession.DispatchEntry:
				delete(holds, x.CallID)
			}
		}
		have := map[string]bool{}
		for _, e := range line {
			if it, ok := e.(*agentsession.ItemEntry); ok {
				if fc, ok := it.Item.(*openresponses.FunctionCall); ok {
					have[fc.CallID] = true
				}
			}
		}
		for _, id := range last.Pending {
			if !have[id] || outputs[id] {
				continue
			}
			if q, held := holds[id]; held && last.Reason == agentsession.ReasonInputRequired {
				out.perms = append(out.perms, id+"|"+q)
			} else {
				out.cuts = append(out.cuts, id)
			}
		}
		sort.Strings(out.perms)
		sort.Strings(out.cuts)
	}
	return out
}

// minimize shrinks a failing script by dropping what it can while a
// delivery seed still fails.
func minimize(sc pScript, fails func(pScript) bool) pScript {
	try := fails
	for rounds, changed := 0, true; changed && rounds < 200; rounds++ {
		changed = false
		for i := range sc.Elems {
			cand := pScript{Elems: append(append([]pElem(nil), sc.Elems[:i]...), sc.Elems[i+1:]...)}
			if len(cand.Elems) > 0 && try(cand) {
				sc, changed = cand, true
				break
			}
		}
		if changed {
			continue
		}
	elems:
		for i, el := range sc.Elems {
			for j := range el.Turns {
				cand := clone(sc)
				cand.Elems[i].Turns = append(append([]pTurn(nil), el.Turns[:j]...), el.Turns[j+1:]...)
				if try(cand) {
					sc, changed = cand, true
					break elems
				}
			}
			for j, t := range el.Turns {
				for k := range t.Items {
					cand := clone(sc)
					cand.Elems[i].Turns[j].Items = append(append([]pItem(nil), t.Items[:k]...), t.Items[k+1:]...)
					if try(cand) {
						sc, changed = cand, true
						break elems
					}
				}
				for k := range t.Calls {
					cand := clone(sc)
					cand.Elems[i].Turns[j].Calls = append(append([]pCall(nil), t.Calls[:k]...), t.Calls[k+1:]...)
					if try(cand) {
						sc, changed = cand, true
						break elems
					}
				}
				for _, simp := range []func(*pTurn){
					func(t *pTurn) { t.Retries = 0 }, func(t *pTurn) { t.Withheld = false }, func(t *pTurn) { t.Cut = false },
					func(t *pTurn) { t.Model = "" }, func(t *pTurn) { t.Failed = false }, func(t *pTurn) { t.Steer = false },
					func(t *pTurn) { t.NameAfter = 0 },
				} {
					cand := clone(sc)
					simp(&cand.Elems[i].Turns[j])
					if fmt.Sprint(cand) != fmt.Sprint(sc) && try(cand) {
						sc, changed = cand, true
						break elems
					}
				}
				for k, it := range t.Items {
					if it.Reuse != 0 || it.Anon || it.Reason {
						cand := clone(sc)
						cand.Elems[i].Turns[j].Items[k] = pItem{}
						if try(cand) {
							sc, changed = cand, true
							break elems
						}
					}
				}
			}
		}
	}
	return sc
}

func clone(sc pScript) pScript {
	out := pScript{Elems: make([]pElem, len(sc.Elems))}
	for i, e := range sc.Elems {
		e.Turns = append([]pTurn(nil), e.Turns...)
		for j := range e.Turns {
			e.Turns[j].Items = append([]pItem(nil), e.Turns[j].Items...)
			e.Turns[j].Calls = append([]pCall(nil), e.Turns[j].Calls...)
		}
		out.Elems[i] = e
	}
	return out
}

// play builds the timeline of a script, or reports it invalid.
func play(sc pScript) (*timeline, bool) {
	tl := &timeline{header: agentsession.Header{ID: "prop"}}
	if err := tl.play(sc); err != nil {
		return nil, false
	}
	return tl, true
}

// failing runs a script under several delivery seeds and reports the first
// failure.
func failing(sc pScript, base int64, tries int) (string, opTrace, int64, bool) {
	tl, ok := play(sc)
	if !ok {
		return "", nil, 0, false
	}
	for i := 0; i < tries; i++ {
		seed := base + int64(i)*7919
		if msg, trace := deliver(tl, seed); msg != "" {
			return msg, trace, seed, true
		}
	}
	return "", nil, 0, false
}

func TestInterleavings(t *testing.T) {
	seeds := envInt("AGENTCONSOLE_SEEDS", 150)
	if testing.Short() {
		seeds = min(seeds, 60)
	}
	seed0 := envInt("AGENTCONSOLE_SEED0", 1)
	invalid := 0
	for s := seed0; s < seed0+seeds; s++ {
		r := rand.New(rand.NewSource(int64(s)))
		sc := genScript(r, profiles[s%len(profiles)])
		msg, trace, dseed, bad := failing(sc, int64(s)*1000003, 4)
		if _, ok := play(sc); !ok {
			invalid++
			continue
		}
		if !bad {
			continue
		}
		small := minimize(sc, func(c pScript) bool { _, _, _, f := failing(c, int64(s)*1000003, 12); return f })
		smsg, strace, sdseed, _ := failing(small, int64(s)*1000003, 12)
		if smsg == "" {
			smsg, strace, sdseed = msg, trace, dseed
			small = sc
		}
		t.Fatalf("seed %d (delivery seed %d) failed:\n%s\nminimized script:\n%s\ntrace:\n  %s",
			s, sdseed, smsg, small, strings.Join(strace, "\n  "))
	}
	if invalid > seeds/4 {
		t.Errorf("%d of %d generated scripts were invalid: the generator is broken", invalid, seeds)
	}
}

// dump is the view's inner state, for a failure report.
func dump(v *View) string {
	var b strings.Builder
	for _, o := range v.items {
		fmt.Fprintf(&b, "item{%s run=%s turn=%d resp=%q done=%v} ", itemText(o.item), o.run, o.turn, o.responseID, o.done)
	}
	fmt.Fprintf(&b, "turn=%+v closed=%v liveEnded=%v", v.turn, v.closed.order, v.liveEnded)
	return b.String()
}

func firstToken(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return s
}

// extends reports whether entry id is at or below ancestor in the
// reference's entries.
func (r rendering) extends(id, ancestor string) bool {
	for hops := 0; id != "" && hops <= len(r.parent); hops++ {
		if id == ancestor {
			return true
		}
		id = r.parent[id]
	}
	return false
}

// liveness: every item the live stream has opened, whose entry no delivery
// has held, is shown from its first event on, under the line its run is
// extending and under no other; an item that was never going to have an
// entry goes when its response ends, a retry happens or its run is settled.
func (c *checker) liveness(m Model, ref rendering, cur map[string]rowInfo) string {
	runEntries := map[string][]string{}
	for id := range ref.entryIDs {
		if run := c.tl.entryRun[id]; run != "" {
			runEntries[run] = append(runEntries[run], id)
		}
	}
	for u, it := range c.items {
		if c.everLanded[u] {
			continue
		}
		info, shown := cur[u]
		shown = shown && info.live
		if c.tl.ephemeral[u] && (c.gone[u] || c.flushed[it.run]) {
			if shown {
				return fmt.Sprintf("live row %s is shown after its response ended, a retry or its run's settling", u)
			}
			continue
		}
		delivered := runEntries[it.run]
		if len(delivered) == 0 {
			continue // none of the run's entries is in: nothing says which line it extends
		}
		on := true
		for _, e := range delivered {
			if !ref.extends(ref.tail, e) {
				on = false
				break
			}
		}
		switch {
		case on && !shown:
			return fmt.Sprintf("item %s was opened on the stream, its entry is not in, and it is not shown", u)
		case !on && shown:
			return fmt.Sprintf("live row %s is shown under a line its run does not extend", u)
		}
	}
	for _, row := range m.Rows {
		if row.Live == (row.EntryID != "") {
			return fmt.Sprintf("row %s: live=%v with entry %q", rowUID(row), row.Live, row.EntryID)
		}
		if row.Live && row.Open == c.completed[rowUID(row)] {
			return fmt.Sprintf("live row %s: open=%v, the stream completed it=%v", rowUID(row), row.Open, c.completed[rowUID(row)])
		}
	}
	return ""
}

// callRows checks each call's row against the record and the stream.
func (c *checker) callRows(m Model, ref rendering) string {
	cut := map[string]bool{}
	for _, p := range m.CutOff {
		cut[p.CallID] = true
	}
	waiting := map[string]bool{}
	for _, p := range m.Permissions {
		waiting[p.CallID] = true
	}
	for _, row := range m.Rows {
		if row.Call == nil {
			continue
		}
		cl := row.Call
		if cl.Name != "tool" || cl.Args != "{}" {
			return fmt.Sprintf("call %s: name %q args %q", cl.CallID, cl.Name, cl.Args)
		}
		if out, ok := ref.outputs[cl.CallID]; ok {
			if !cl.Committed || cl.State != CallEnded || cl.Output != out {
				return fmt.Sprintf("call %s has its output on the record (%q) and renders %+v", cl.CallID, out, *cl)
			}
			continue
		}
		if cl.Committed {
			return fmt.Sprintf("call %s is committed with no output on the viewed line", cl.CallID)
		}
		t := c.calls[cl.CallID]
		if t == nil || c.flushed[t.run] || c.everOut[cl.CallID] || cut[cl.CallID] || waiting[cl.CallID] {
			continue
		}
		switch {
		case t.finished && !t.deferred:
			if cl.State != CallEnded || cl.Output != t.result {
				return fmt.Sprintf("call %s finished on the stream with %q and renders %+v", cl.CallID, t.result, *cl)
			}
		case t.dispatched:
			if cl.State != CallRunning {
				return fmt.Sprintf("call %s is running and renders %+v", cl.CallID, *cl)
			}
			if t.partial != "" && cl.Partial != t.partial {
				return fmt.Sprintf("call %s progress %q renders %q", cl.CallID, t.partial, cl.Partial)
			}
		}
		if len(t.children) != len(cl.Children) {
			return fmt.Sprintf("call %s made %d calls and renders %d", cl.CallID, len(t.children), len(cl.Children))
		}
		for _, ch := range cl.Children {
			ct := c.calls[ch.CallID]
			if ct == nil || ct.parent != cl.CallID || ch.Name != "child" {
				return fmt.Sprintf("call %s renders a child %+v the stream did not make", cl.CallID, ch)
			}
			if ct.finished && (ch.State != CallEnded || ch.Output != ct.result) {
				return fmt.Sprintf("child %s finished with %q and renders %+v", ch.CallID, ct.result, ch)
			}
		}
	}
	return ""
}

// TestGeneratorCoverage keeps the generator honest: every kind of thing the
// property test claims to play shows up in the timelines of a few hundred
// seeds, so a generator that quietly stops producing one fails here rather
// than leaving the invariants unexercised.
func TestGeneratorCoverage(t *testing.T) {
	seen := map[string]int{}
	for s := 1; s <= 400; s++ {
		sc := genScript(rand.New(rand.NewSource(int64(s))), profiles[s%len(profiles)])
		tl, ok := play(sc)
		if !ok {
			continue
		}
		if sc.LateCfg {
			seen["late config"]++
		}
		for _, el := range sc.Elems {
			for _, tn := range el.Turns {
				if !tn.Named && tn.NameAfter > 0 && len(tn.Items)+len(tn.Calls) > tn.NameAfter {
					seen["named mid-stream"]++
				}
			}
		}
		for _, op := range tl.ops {
			if op.isHead {
				seen["head record"]++
			}
		}
		for _, e := range tl.entries {
			switch x := e.(type) {
			case *agentsession.CustomEntry:
				if _, _, ok := session.MarkedItem(x); ok {
					seen["marked item"]++
				}
			case *agentsession.ItemEntry:
				if !x.IsVisible() {
					seen["hidden item"]++
				}
				if x.QueuedFrom != "" {
					seen["steered input"]++
				}
			case *agentsession.LabelEntry:
				if x.Label != nil && *x.Label == "bookmark" {
					seen["bookmark"]++
				}
				if x.Label != nil && *x.Label == agentsession.LeafLabel {
					seen["leaf label"]++
				}
			case *agentsession.LinkEntry:
				seen["link entry"]++
			case *agentsession.CompactionEntry:
				seen["compaction"]++
			case *agentsession.ResponseEntry:
				if x.Status == openresponses.ResponseStatusFailed && x.ResponseID == "" {
					seen["cut response with no ID"]++
				}
			}
		}
		for _, l := range tl.lives {
			switch x := l.ev.(type) {
			case *client.ToolProgress:
				seen["tool progress"]++
			case *client.ToolOpened:
				if x.Parent != "" {
					seen["child call"]++
				}
			case *client.ModelRetrying:
				seen["retry"]++
			case *client.ResponseCompleted:
				if x.Withheld {
					seen["withheld"]++
				}
			case *client.ItemOpened:
				if x.ResponseID == "" {
					if _, isMsg := x.Item.(*openresponses.Message); isMsg {
						seen["unnamed stream"]++
					}
				}
			case *client.RunEnded:
				if x.Reason == agentturn.ReasonInputRequired {
					seen["input required"]++
				}
				if x.Reason == agentturn.ReasonAborted {
					seen["aborted"]++
				}
				if x.Reason == agentturn.ReasonError {
					seen["failed"]++
				}
			}
		}
	}
	for _, k := range []string{"late config", "head record", "marked item", "hidden item", "steered input", "bookmark", "leaf label",
		"cut response with no ID", "named mid-stream", "link entry", "compaction", "tool progress", "child call", "retry", "withheld", "unnamed stream", "input required", "aborted", "failed"} {
		if seen[k] == 0 {
			t.Errorf("the generator never produced %q in 400 scripts", k)
		}
	}
	t.Logf("coverage over 400 scripts: %v", seen)
}

// treeFacts checks what the tree and the detail panes read off the model
// against the reference renderer: the branches, the tail of the line, the
// links and the compaction rows.
func (c *checker) treeFacts(m Model, ref rendering) string {
	if m.Tail != ref.tail {
		return fmt.Sprintf("tail %s, want %s", m.Tail, ref.tail)
	}
	if m.Entries != ref.n {
		return fmt.Sprintf("%d entries, the record has %d", m.Entries, ref.n)
	}
	if len(m.Branches) != len(ref.branches) {
		return fmt.Sprintf("%d branches %v, want %d %v", len(m.Branches), m.Branches, len(ref.branches), ref.branches)
	}
	for i, b := range m.Branches {
		w := ref.branches[i]
		if b.Leaf != w.leaf || b.Current != w.current || b.Role != w.role {
			return fmt.Sprintf("branch %d is %+v, want %+v", i, b, w)
		}
		if (w.role == "") != (b.Label == "(no message)") {
			return fmt.Sprintf("branch %d label %q with role %q", i, b.Label, w.role)
		}
	}
	if len(m.Links) != len(ref.links) {
		return fmt.Sprintf("links %v, want %v", m.Links, ref.links)
	}
	for i, l := range m.Links {
		w := ref.links[i]
		if l.Rel != w.rel || l.Session != w.session || l.CallID != w.callID || l.Entry != w.entry {
			return fmt.Sprintf("link %d is %+v, want %+v", i, l, w)
		}
	}
	var folds []Row
	for _, row := range m.Rows {
		if row.Fold != nil {
			folds = append(folds, row)
		}
	}
	if len(folds) != len(ref.folds) {
		return fmt.Sprintf("%d compaction rows, want %d", len(folds), len(ref.folds))
	}
	for i, row := range folds {
		w := ref.folds[i]
		if row.EntryID != w.entry || row.Fold.FirstKept != w.firstKept || row.Fold.SummaryLen != w.summaryLen || row.Fold.Pinned != w.pinned {
			return fmt.Sprintf("compaction row %d is %+v (entry %s), want %+v", i, row.Fold, row.EntryID, w)
		}
	}
	return ""
}
