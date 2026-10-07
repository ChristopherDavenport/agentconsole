package inspect

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/agentturn/session"
	"github.com/ChristopherDavenport/openresponses"
)

// Entry is what the session says about one entry of the viewed line.
type Entry struct {
	ID   string
	Type string
	Time time.Time
	// Title says what the entry is: "agent message", "function call
	// upper", "compaction".
	Title string
	// KeptFromModel is set for an item the filter kept out of the
	// model's context.
	KeptFromModel bool
	// Response is the response the entry's item belongs to, when it has
	// one.
	Response *Response
	// Call is set for a function call and for its output.
	Call *Call
	// Fold is set for a compaction.
	Fold *Fold
}

// Response is a response entry and how it verifies.
type Response struct {
	Entry       string
	ResponseID  string
	Model       string
	Status      string
	Usage       *openresponses.Usage
	LatencyMS   int64
	Attempts    int
	RequestHash string
	Verify      Verify
}

// Call is a function call as the path holds it.
type Call struct {
	CallID string
	Name   string
	Args   string
	// State is the path's reading of the call (agentsession's CallState).
	State string
	// Decisions are the decision entries for the call, in order: each the
	// verdict, who gave it and why.
	Decisions []Decision
	// Verdicts are the policy verdicts recorded for the call
	// (agentpolicy:verdict): the rule and the source behind a decision.
	Verdicts []Verdict
	// Asked are the questions put to the user about the call, and what
	// became of them.
	Asked []session.Elicitation
	// Dispatch is the dispatch entry, when the call was handed to its tool.
	Dispatch *Dispatch
	// Output is what the call returned, and OutputEntry the entry that
	// holds it; HasOutput says whether there is one.
	Output      string
	OutputEntry string
	HasOutput   bool
	// Grants are the skill grants in force when the call was made, and
	// whether and when they were revoked later.
	Grants []Grant
}

// Decision is a decision entry.
type Decision struct {
	Entry   string
	Verdict string
	By      string
	Reason  string
	Args    string
}

// Dispatch is a dispatch entry.
type Dispatch struct {
	Entry          string
	Target         string
	IdempotencyKey string
	Time           time.Time
}

// Fold is what a compaction entry folded.
type Fold struct {
	// FirstKept is the entry the context keeps from, described.
	FirstKept     string
	FirstKeptDesc string
	// Folded is how many items sit on the path before FirstKept: the ones
	// the summary stands for in the model's request. They stay on the
	// record.
	Folded int
	// SummaryLen is the summary's length in runes, and SummaryType the
	// item type that carries it.
	SummaryLen   int
	SummaryType  string
	SummaryStart string
	// Pinned describes the items carried over whole.
	Pinned       []string
	TokensBefore int
	Usage        *openresponses.Usage
	// Call is the model call the fold made, when the compaction records one.
	Call *session.FoldCall
}

// Entry computes the detail of the entry, on the line of the session that
// ends at tail. entries is how many entries the viewer's model held, so a
// snapshot read for an earlier one is not reused.
func (in *Inspector) Entry(ctx context.Context, sessionID, tail, entryID string, entries int) (Entry, error) {
	s, err := in.snapshot(ctx, sessionID, entries)
	if err != nil {
		return Entry{}, err
	}
	if tail == "" {
		tail = s.Leaf()
	}
	path := s.Path(tail)
	at := -1
	for i, e := range path {
		if e.Base().ID == entryID {
			at = i
		}
	}
	if at < 0 {
		return Entry{}, fmt.Errorf("inspect: entry %s is not on the viewed line", entryID)
	}
	e := path[at]
	out := Entry{ID: entryID, Type: e.EntryType(), Time: e.Base().Timestamp}
	switch x := e.(type) {
	case *agentsession.ItemEntry:
		out.Title = itemTitle(x.Item)
		in.fill(&out, s, path, at, x.Item, x.ResponseID)
	case *agentsession.CustomEntry:
		item, resp, ok := session.MarkedItem(x)
		if !ok {
			out.Title = "custom entry " + x.NS
			break
		}
		out.KeptFromModel = true
		out.Title = itemTitle(item)
		in.fill(&out, s, path, at, item, resp)
	case *agentsession.CompactionEntry:
		out.Title = "compaction"
		out.Fold = foldOf(path, at, x)
	case *agentsession.ResponseEntry:
		out.Title = "response"
		out.Response = in.responseOf(s, path, at)
	default:
		out.Title = e.EntryType() + " entry"
	}
	return out, nil
}

// fill adds what an item's entry has: the response that produced it and,
// for a call or an output, the call.
func (in *Inspector) fill(out *Entry, s *agentsession.Session, path []agentsession.Entry, at int, item openresponses.Item, responseID string) {
	if responseID != "" {
		for i := at + 1; i < len(path); i++ {
			if r, ok := path[i].(*agentsession.ResponseEntry); ok && r.ResponseID == responseID {
				out.Response = in.responseOf(s, path, i)
				break
			}
		}
	}
	var callID string
	switch it := item.(type) {
	case *openresponses.FunctionCall:
		callID = it.CallID
	case *openresponses.FunctionCallOutput:
		callID = it.CallID
	default:
		return
	}
	for _, c := range agentsession.Calls(path) {
		if c.ID() != callID {
			continue
		}
		out.Call = callOf(s, path, c)
	}
	if out.Call == nil {
		// An output whose call is not on the path, or a call the path cut.
		out.Call = &Call{CallID: callID, State: "unknown"}
	}
}

func (in *Inspector) responseOf(s *agentsession.Session, path []agentsession.Entry, i int) *Response {
	r := path[i].(*agentsession.ResponseEntry)
	return &Response{
		Entry: r.ID, ResponseID: r.ResponseID, Model: r.Model, Status: string(r.Status),
		Usage: r.Usage, LatencyMS: r.LatencyMS, Attempts: r.Calls(), RequestHash: r.RequestHash,
		Verify: in.verifyResponse(s, path, i),
	}
}

func callOf(s *agentsession.Session, path []agentsession.Entry, c *agentsession.Call) *Call {
	out := &Call{CallID: c.ID(), Name: c.Call.Name, Args: c.Call.Arguments, State: c.State(s.Header()).String()}
	for _, d := range c.Decisions {
		out.Decisions = append(out.Decisions, Decision{Entry: d.ID, Verdict: d.Verdict, By: d.By, Reason: d.Reason, Args: string(d.Args)})
	}
	if c.Dispatch != nil {
		out.Dispatch = &Dispatch{Entry: c.Dispatch.ID, Target: c.Dispatch.Target, IdempotencyKey: c.Dispatch.IdempotencyKey, Time: c.Dispatch.Timestamp}
	}
	if c.Output != nil {
		out.OutputEntry, out.HasOutput = c.Output.ID, true
		if o, ok := c.Output.Item.(*openresponses.FunctionCallOutput); ok {
			out.Output = o.Output.String()
		}
	}
	// The grants in force are those at the call's last decision: in a
	// batch from one response every call's item is written before any skill
	// read lands its grant, and a call decided after it runs under it.
	// Without a decision entry, the call's item stands in.
	at := -1
	last := c.Entry.ID
	if n := len(c.Decisions); n > 0 {
		last = c.Decisions[n-1].ID
	}
	for i, e := range path {
		if e.Base().ID == last {
			at = i
		}
		if v, ok := verdictOf(e); ok && v.CallID == c.ID() {
			out.Verdicts = append(out.Verdicts, v)
		}
	}
	out.Asked = elicitationsFor(path, c.ID())
	if at >= 0 {
		out.Grants = grantsAt(path, at)
	}
	return out
}

func foldOf(path []agentsession.Entry, at int, c *agentsession.CompactionEntry) *Fold {
	f := &Fold{FirstKept: c.FirstKept, TokensBefore: c.TokensBefore, Usage: c.Usage}
	f.SummaryType = c.Summary.ItemType()
	if text, ok := itemText(c.Summary); ok {
		f.SummaryLen, f.SummaryStart = len([]rune(text)), firstWords(text)
	} else if cm, ok := c.Summary.(*openresponses.Compaction); ok {
		f.SummaryLen = len(cm.EncryptedContent)
	}
	for _, p := range c.Pinned {
		f.Pinned = append(f.Pinned, itemTitle(p))
	}
	for _, e := range path[:at] {
		if e.Base().ID == c.FirstKept {
			f.FirstKeptDesc = describeEntry(e)
			break
		}
		if ie, ok := e.(*agentsession.ItemEntry); ok && ie.IsVisible() {
			f.Folded++
		}
	}
	if f.FirstKeptDesc == "" {
		f.FirstKeptDesc = "(not on this line)"
	}
	if raw, ok := c.Unknown[session.FoldMember]; ok {
		var fc session.FoldCall
		if json.Unmarshal(raw, &fc) == nil {
			f.Call = &fc
		}
	}
	return f
}

func describeEntry(e agentsession.Entry) string {
	if ie, ok := e.(*agentsession.ItemEntry); ok {
		if text, ok := itemText(ie.Item); ok {
			return itemTitle(ie.Item) + ": " + firstWords(text)
		}
		return itemTitle(ie.Item)
	}
	return e.EntryType()
}

func itemText(item openresponses.Item) (string, bool) {
	switch v := item.(type) {
	case *openresponses.Message:
		return v.Text(), true
	case *openresponses.ReasoningItem:
		t := v.Summary.Text()
		if t == "" {
			t = v.Content.Text()
		}
		return t, true
	}
	return "", false
}

func itemTitle(item openresponses.Item) string {
	switch v := item.(type) {
	case *openresponses.Message:
		if v.Role == openresponses.RoleAssistant {
			return "agent message"
		}
		return string(v.Role) + " message"
	case *openresponses.ReasoningItem:
		return "reasoning"
	case *openresponses.FunctionCall:
		return "function call " + v.Name
	case *openresponses.FunctionCallOutput:
		return "function call output"
	}
	return item.ItemType()
}

func firstWords(text string) string {
	words := strings.Fields(text)
	more := len(words) > 10
	if more {
		words = words[:10]
	}
	out := strings.Join(words, " ")
	if r := []rune(out); len(r) > 80 {
		out, more = string(r[:80]), true
	}
	if more {
		out += "…"
	}
	return out
}
