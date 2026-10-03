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
//	AGENTCONSOLE_SEEDS=20000 go test ./internal/view -run TestInterleavings
//	AGENTCONSOLE_SEED0=1234 AGENTCONSOLE_SEEDS=1 go test ./internal/view -run TestInterleavings -v
//
// replays a seed. The default is a few hundred seeds.

import (
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn"
	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/internal/client"
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
	models := []string{"m1", "m2", "m3"}
	n := 1 + r.Intn(4)
	for i := 0; i < n; i++ {
		if i > 0 && r.Intn(100) < pf.rewind {
			sc.Elems = append(sc.Elems, pElem{Kind: "rewind", To: r.Intn(50)})
			continue
		}
		el := pElem{Kind: "run", Approve: r.Intn(2) == 0}
		if i == 0 || r.Intn(100) < 25 {
			el.Model = models[r.Intn(len(models))]
		}
		for t := 0; t < 1+r.Intn(3); t++ {
			turn := pTurn{Named: r.Intn(2) == 0}
			if r.Intn(100) < pf.modelSwitch {
				turn.Model = models[r.Intn(len(models))]
			}
			for k := 0; k < 1+r.Intn(2); k++ {
				it := pItem{Reuse: r.Intn(pf.reuse), Reason: r.Intn(4) == 0, Anon: r.Intn(10) == 0}
				turn.Items = append(turn.Items, it)
			}
			if r.Intn(100) < pf.calls {
				for k := 0; k < 1+r.Intn(2); k++ {
					turn.Calls = append(turn.Calls, pCall{Defer: r.Intn(100) < pf.deferred, Hang: r.Intn(100) < 12})
				}
			}
			switch x := r.Intn(100); {
			case x < 8:
				turn.Retries = 1 + r.Intn(2)
			case x < 14:
				turn.Withheld = true
			case x < 20:
				turn.Cut = true
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

// build reads a session holding the first k entries.
func (f *follower) build(k int) error {
	fs := agentsession.New(f.tl.header)
	for i := 0; i < k; i++ {
		c, err := copyEntry(f.tl.entries[i])
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

// next extends the session with the next entry and returns the changes a
// follower yields for it.
func (f *follower) next() ([]agentsession.Change, error) {
	c, err := copyEntry(f.tl.entries[f.entries])
	if err != nil {
		return nil, err
	}
	before := f.fs.Leaf()
	r, err := f.fs.Extend(c)
	if err != nil {
		return nil, fmt.Errorf("extend %d: %w", f.entries, err)
	}
	f.entries++
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
	n, m := len(tl.entries), len(tl.lives)

	k0 := r.Intn(n + 1)
	if err := f.build(k0); err != nil {
		return "setup: " + err.Error(), trace
	}
	trace.add("snapshot at %d of %d entries", k0, n)
	v.Record(agentsession.Change{Kind: agentsession.Snapshot, Session: f.fs})
	c := &checker{tl: tl, v: v, f: f, prev: map[string]rowInfo{}, openEph: map[string]bool{}, gone: map[string]bool{}}
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
			trace.add("record entry %d %s", f.entries, tl.entries[f.entries].EntryType())
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
	tl      *timeline
	v       *View
	f       *follower
	prev    map[string]rowInfo
	running bool
	prevLf  string
	started bool
	lastEnd *client.RunEnded
	openEph map[string]bool
	gone    map[string]bool
	// standing is the run whose live end was delivered before the
	// record's end entry for it: its facts stand until that entry lands.
	// An end entry that landed first, and was taken back by a reset
	// later, leaves nothing standing.
	standing  string
	justEnded bool
}

func (c *checker) liveDelivered(ev client.LiveEvent) {
	eph := func(item openresponses.Item) {
		if u := rowUID(Row{Item: item}); c.tl.ephemeral[u] {
			c.openEph[u] = true
		}
	}
	switch e := ev.(type) {
	case *client.RunStarted:
		c.running = true
		clear(c.openEph) // a cut stream's leftovers go with their run, not a later response
	case *client.RunEnded:
		c.running = false
		clear(c.openEph)
		c.lastEnd = e
		c.justEnded = true
	case *client.ItemOpened:
		eph(e.Item)
	case *client.ItemUpdated:
		eph(e.Item)
	case *client.ModelRetrying, *client.ResponseCompleted:
		// What the turn streamed and never completed has no entry
		// coming: it must be gone from here on, not at the run's end.
		for u := range c.openEph {
			c.gone[u] = true
		}
		clear(c.openEph)
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
	// A row once shown is not dropped before its entry lands, unless its
	// item was never going to have one, or the viewed path moved.
	inLog := ref.uids
	for u, info := range c.prev {
		if _, ok := cur[u]; ok || c.tl.ephemeral[u] {
			continue
		}
		switch {
		case moved && !info.live:
			// A committed row leaves with the path it was on.
		case moved && info.live && inLog[u]:
			// A live row whose entry the new session holds, on another
			// branch.
		case !info.live && c.leftThePath(ref):
			// A new entry moved the viewed leaf off the old path.
		default:
			return fail("row %s (live=%v) was rendered and then dropped", u, info.live)
		}
	}
	c.prev = cur
	c.prevLf = m.Leaf
	for u := range c.gone {
		if _, ok := cur[u]; ok {
			return fail("row %s was dropped by its response's end or a retry and is back", u)
		}
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
		if id == c.prevLf {
			return false
		}
	}
	return true
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
}

func render(s *agentsession.Session) rendering {
	var out rendering
	entries := s.Entries()
	byID := map[string]agentsession.Entry{}
	kids := map[string][]string{}
	out.uids = map[string]bool{}
	out.allRuns, out.endRuns = map[string]bool{}, map[string]bool{}
	for _, e := range entries {
		if r, ok := e.(*agentsession.RunEntry); ok {
			out.allRuns[r.RunID] = true
			if r.IsEnd() {
				out.endRuns[r.RunID] = true
			}
		}
		b := e.Base()
		byID[b.ID] = e
		kids[b.Parent] = append(kids[b.Parent], b.ID)
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
	for _, e := range pathTo(out.leaf) {
		out.pathIDs = append(out.pathIDs, e.Base().ID)
		if it, ok := e.(*agentsession.ItemEntry); ok && it.IsVisible() {
			out.rows = append(out.rows, rowUID(Row{Item: it.Item}))
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
	try := func(cand pScript) bool {
		if fails(cand) {
			return true
		}
		return false
	}
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
					func(t *pTurn) { t.Model = "" },
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
	seeds := envInt("AGENTCONSOLE_SEEDS", 300)
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
