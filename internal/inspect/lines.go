package inspect

import (
	"fmt"
	"strings"
	"time"

	"github.com/ChristopherDavenport/openresponses"
)

// maxListed bounds a list in a pane: the pane is read at a glance, and the
// record has the rest.
const maxListed = 8

// short is an ID cut for a pane: the hash's prefix.
func short(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func usageLine(u *openresponses.Usage) string {
	if u == nil {
		return "not recorded"
	}
	s := fmt.Sprintf("%d in, %d out, %d total", u.InputTokens, u.OutputTokens, u.TotalTokens)
	if c := u.InputTokensDetails.CachedTokens; c > 0 {
		s += fmt.Sprintf(", %d cached", c)
	}
	if r := u.OutputTokensDetails.ReasoningTokens; r > 0 {
		s += fmt.Sprintf(", %d reasoning", r)
	}
	return s
}

// Lines renders the entry's detail as plain lines: a heading, then
// "label: value" lines and indented lists.
func (e Entry) Lines() []string {
	var l []string
	add := func(format string, a ...any) { l = append(l, fmt.Sprintf(format, a...)) }
	add("%s", e.Title)
	add("entry:    %s", e.ID)
	if !e.Time.IsZero() {
		add("written:  %s", e.Time.Local().Format(time.DateTime))
	}
	if e.KeptFromModel {
		add("note:     kept from the model: the context leaves it out of every request")
	}
	if c := e.Call; c != nil {
		add("")
		add("call %s %s", c.CallID, c.Name)
		add("  state:    %s", c.State)
		if c.Args != "" {
			add("  args:     %s", clip(c.Args, 200))
		}
		if len(c.Decisions) == 0 {
			add("  decision: none recorded (the session records no decision entries for it)")
		}
		for _, d := range c.Decisions {
			by := d.By
			if by == "" {
				by = "unknown"
			}
			s := fmt.Sprintf("  decision: %s by %s", d.Verdict, by)
			if d.Reason != "" {
				s += ", " + clip(d.Reason, 160)
			}
			l = append(l, s)
			if d.Args != "" {
				add("            with arguments %s", clip(d.Args, 160))
			}
		}
		for _, v := range c.Verdicts {
			s := fmt.Sprintf("  policy:   %s", v.Action)
			if v.Rule != "" {
				s += " by rule " + v.Rule
			}
			if v.Source != "" {
				s += " from " + v.Source
			}
			if v.By != "" {
				s += ", by " + v.By
			}
			l = append(l, s)
			if v.Note != "" {
				add("            note: %s", clip(v.Note, 160))
			}
			if v.Reason != "" {
				add("            reason: %s", clip(v.Reason, 160))
			}
		}
		for _, q := range c.Asked {
			if q.Phase == "ask" {
				add("  asked:    %s", clip(q.Message, 160))
			} else {
				add("  answered: %s by %s", orNone(q.Action), orNone(q.By))
			}
		}
		if d := c.Dispatch; d != nil {
			s := fmt.Sprintf("  dispatch: to %s (entry %s)", orNone(d.Target), short(d.Entry))
			if d.IdempotencyKey != "" {
				s += ", key " + d.IdempotencyKey
			}
			l = append(l, s)
		} else {
			add("  dispatch: none recorded")
		}
		if c.HasOutput {
			add("  output:   %s (entry %s)", clip(c.Output, 200), short(c.OutputEntry))
		} else {
			add("  output:   none yet")
		}
		if len(c.Grants) == 0 {
			add("  grants:   no skill grant in force")
		}
		for _, g := range c.Grants {
			s := fmt.Sprintf("  grant:    %s allowed %s (entry %s)", g.Skill, g.Rule, short(g.Since))
			if g.Ended != "" {
				s += fmt.Sprintf(", revoked later at %s", short(g.Ended))
			}
			l = append(l, s)
		}
	}
	if r := e.Response; r != nil {
		add("")
		add("response %s (entry %s)", r.ResponseID, short(r.Entry))
		add("  model:    %s", orNone(r.Model))
		add("  status:   %s", orNone(r.Status))
		add("  usage:    %s", usageLine(r.Usage))
		if r.LatencyMS > 0 {
			add("  latency:  %d ms", r.LatencyMS)
		}
		if r.Attempts > 1 {
			add("  attempts: %d model calls", r.Attempts)
		}
		switch r.Verify.State {
		case Verified:
			add("  request:  verified, the path rebuilds the request that hashes to %s", short(r.RequestHash))
		case Unhashed:
			add("  request:  UNHASHED, no request hash recorded")
			add("            why: %s", r.Verify.Why)
		default:
			add("  request:  %s: %s", r.Verify.State, r.Verify.Why)
		}
	} else if e.Type == "item" || e.KeptFromModel {
		add("response: none (an input item, or its response is not on the line yet)")
	}
	if f := e.Fold; f != nil {
		add("")
		add("folded:    %d items before the first kept entry stay on the record; the request carries the summary instead", f.Folded)
		add("first kept: %s (%s)", short(f.FirstKept), f.FirstKeptDesc)
		add("summary:   %s, %d chars", f.SummaryType, f.SummaryLen)
		if f.SummaryStart != "" {
			add("            %s", f.SummaryStart)
		}
		if len(f.Pinned) == 0 {
			add("pinned:    none")
		} else {
			add("pinned:    %d items carried over whole", len(f.Pinned))
			for i, p := range f.Pinned {
				if i == maxListed {
					add("            ... %d more", len(f.Pinned)-maxListed)
					break
				}
				add("            %s", p)
			}
		}
		if f.TokensBefore > 0 {
			add("tokens:    about %d before the fold", f.TokensBefore)
		}
		if f.Usage != nil {
			add("usage:     %s", usageLine(f.Usage))
		}
		if c := f.Call; c != nil {
			add("fold call: model %s, response %s", orNone(c.Model), orNone(c.ResponseID))
		}
	}
	return l
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// Lines renders the session pane as plain lines.
func (s Session) Lines() []string {
	var l []string
	add := func(format string, a ...any) { l = append(l, fmt.Sprintf(format, a...)) }
	title := "session " + s.ID
	if s.Name != "" {
		title += " (" + s.Name + ")"
	}
	add("%s", title)
	add("cwd:      %s", orNone(s.CWD))
	add("harness:  %s", orNone(s.Harness))
	add("format:   %s", orNone(s.Format))
	if !s.Created.IsZero() {
		add("created:  %s", s.Created.Local().Format(time.DateTime))
	}
	add("entries:  %d, %d on the viewed line", s.Entries, s.Path)
	if s.ParentSession != "" {
		o := "from " + s.ParentSession
		if s.Base != "" {
			o += " at " + short(s.Base)
		}
		add("origin:   %s", o)
	}
	if s.SpawnedBy != "" {
		add("spawned:  by call %s", s.SpawnedBy)
	}
	v := s.Verify
	switch {
	case v.Responses == 0:
		add("verify:   no responses on the line")
	case v.OK() && v.Unhashed == 0:
		add("verify:   OK, %d responses, every request rebuilt and matched", v.Responses)
	case v.OK():
		add("verify:   no mismatch, %d responses: %d verified, %d unhashed", v.Responses, v.Verified, v.Unhashed)
	default:
		add("verify:   FAILED, %d responses: %d verified, %d unhashed, %d mismatched, %d failed", v.Responses, v.Verified, v.Unhashed, v.Mismatched, v.Failed)
	}
	for i, p := range v.Problems {
		if i == maxListed {
			add("          ... %d more", len(v.Problems)-maxListed)
			break
		}
		add("          %s %s: %s", short(p.Response), p.Verify.State, clip(p.Verify.Why, 140))
	}
	c := s.Config
	switch {
	case c.Known:
		add("config:   model %s, %d instruction parts (%d chars), %d tools", orNone(c.Model), c.InstructionsParts, c.InstructionsLen, c.Tools)
	case c.Err != "":
		add("config:   unknown: %s", c.Err)
	default:
		add("config:   none recorded")
	}
	switch {
	case s.RefsErr != "":
		add("refs:     unavailable, %s", s.RefsErr)
	case len(s.Refs) == 0:
		add("refs:     none point here")
	default:
		for i, r := range s.Refs {
			label := "refs:    "
			if i > 0 {
				label = "         "
			}
			t := ""
			if r.Target.Entry != "" {
				t = " @ " + short(r.Target.Entry)
			}
			add("%s %s%s", label, r.Name, t)
		}
	}
	if m := s.Manifest; m != nil {
		add("memory:   manifest in force: %d entries, %d left out", len(m.Entries), len(m.Omitted))
		for i, e := range m.Entries {
			if i == maxListed {
				add("          ... %d more", len(m.Entries)-maxListed)
				break
			}
			add("          %s/%s, %d bytes", e.Scope, e.Name, e.Bytes)
		}
		for i, e := range m.Omitted {
			if i == maxListed {
				add("          ... %d more left out", len(m.Omitted)-maxListed)
				break
			}
			add("          left out %s/%s: %s", e.Scope, e.Name, orNone(e.Reason))
		}
	} else {
		add("memory:   no manifest on the line")
	}
	if s.ManifestSkipped > 0 {
		add("          %d manifest records did not fold", s.ManifestSkipped)
	}
	if len(s.Grants) == 0 {
		add("grants:   no skill grants on the line")
	}
	for i, g := range s.Grants {
		if i == maxListed {
			add("          ... %d more", len(s.Grants)-maxListed)
			break
		}
		state := "in force"
		if g.Ended != "" {
			state = "revoked at " + short(g.Ended)
		}
		add("grants:   %s allowed %s, %s", g.Skill, g.Rule, state)
	}
	return l
}
