package tui

import (
	"regexp"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ChristopherDavenport/openresponses"

	"github.com/ChristopherDavenport/agentconsole/view"
)

// plain is s with its escape codes stripped and each line's trailing
// spaces trimmed, as a reader sees it.
func plain(s string) string {
	lines := strings.Split(ansi.Strip(s), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}

func TestMarkdownDrawsTheMarkupNotItsMarkers(t *testing.T) {
	src := strings.Join([]string{
		"# Plan",
		"",
		"Some **bold**, *italic*, ~~gone~~ and `code`.",
		"",
		"## Steps",
		"",
		"- one",
		"  - nested",
		"",
		"1. first",
		"2. second",
		"",
		"> quoted",
		"",
		"```go",
		"func main() {}",
		"```",
	}, "\n")
	got := plain(newMarkdown().render(src, 40))
	for _, want := range []string{"Plan", "Some bold, italic, gone and \u00a0code\u00a0.", "Steps", "• one", "  • nested", "1. first", "2. second", "▎quoted", "  func main() {}"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered without %q:\n%s", want, got)
		}
	}
	for _, marker := range []string{"#", "**", "~~", "`", "> "} {
		if strings.Contains(got, marker) {
			t.Errorf("rendered with the marker %q:\n%s", marker, got)
		}
	}
	if lines := strings.Split(got, "\n"); lines[0] != " Plan" || lines[len(lines)-1] != "  func main() {}" {
		t.Errorf("rendered with blank lines around it:\n%q", got)
	}
}

func TestMarkdownCodeIsAShadeOffTheText(t *testing.T) {
	for _, tc := range []struct {
		dark   bool
		fg, bg string
	}{{true, "38;5;250", "48;5;236"}, {false, "38;5;238", "48;5;254"}} {
		md := newMarkdown()
		md.setDark(tc.dark)
		if out := md.render("`code`", 40); !strings.Contains(out, tc.fg) || !strings.Contains(out, tc.bg) {
			t.Errorf("dark %v: inline code rendered %q, want %s on %s", tc.dark, out, tc.fg, tc.bg)
		}
	}
}

func TestMarkdownLinksAreHyperlinks(t *testing.T) {
	out := newMarkdown().render("see [the docs](https://example.com/docs)", 80)
	if !strings.Contains(out, ansi.SetHyperlink("https://example.com/docs")) && !strings.Contains(out, "\x1b]8;id=") {
		t.Errorf("no hyperlink in %q", out)
	}
	if got := plain(out); !strings.Contains(got, "the docs") || !strings.Contains(got, "https://example.com/docs") {
		t.Errorf("rendered %q, want the link's text and its address", got)
	}
}

func TestMarkdownExpandsTabs(t *testing.T) {
	out := newMarkdown().render("```\nif x {\n\ty\n}\n```", 40)
	if strings.Contains(out, "\t") {
		t.Errorf("a tab is left in %q", out)
	}
	if got := plain(out); !strings.Contains(got, "      y") {
		t.Errorf("the tab is not four columns:\n%s", got)
	}
	if got := expandTabs("ab\tc\n\td"); got != "ab  c\n    d" {
		t.Errorf("expandTabs = %q", got)
	}
}

// sgr matches one SGR sequence.
var sgr = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestMarkdownLinesFitAndCloseWhatTheyOpen(t *testing.T) {
	src := "# A heading long enough to wrap\n\nText with **bold that runs on**, a [link that wraps](https://example.com) and `some code`.\n\n- a list item that wraps\n\n> a quote that wraps over a few lines at the narrow widths\n\n| col | other |\n|--|--|\n| cell text | more cell text |\n\n```go\nfunc main() { println(\"long\") }\n```"
	for width := 12; width <= 60; width++ {
		for i, l := range strings.Split(newMarkdown().render(src, width), "\n") {
			if w := lipgloss.Width(l); w > width {
				t.Errorf("width %d: line %d is %d wide: %q", width, i, w, l)
			}
			if seqs := sgr.FindAllString(l, -1); len(seqs) > 0 {
				if last := seqs[len(seqs)-1]; last != "\x1b[m" && last != "\x1b[0m" {
					t.Errorf("width %d: line %d leaves a style open: %q", width, i, l)
				}
			}
			if p := plain(l); strings.Contains(p, "quote") || strings.Contains(p, "narrow") {
				if !strings.HasPrefix(p, "▎") {
					t.Errorf("width %d: a quote's line %q has no bar", width, p)
				}
			}
		}
	}
}

func TestMarkdownKeepsWhatTheLastLayoutRendered(t *testing.T) {
	md := newMarkdown()
	md.render("one", 40)
	md.sweep()
	if _, ok := md.cache[mdKey{"one", 40}]; !ok {
		t.Fatal("a rendering is not kept for the next layout")
	}
	md.render("two", 40)
	md.sweep()
	if _, ok := md.cache[mdKey{"one", 40}]; ok {
		t.Error("a rendering the last layout did not use is still kept")
	}
	if _, ok := md.cache[mdKey{"two", 40}]; !ok {
		t.Error("the last layout's rendering is not kept")
	}
}

func TestOnlyTheAssistantsMessagesAreMarkdown(t *testing.T) {
	msg := func(role openresponses.Role, text string) view.Row {
		return view.Row{EntryID: string(role), Item: &openresponses.Message{Role: role, Content: openresponses.Contents{&openresponses.OutputText{Text: text}}}}
	}
	m := view.Model{Rows: []view.Row{
		msg(openresponses.RoleUser, "**as typed**"),
		msg(openresponses.RoleAssistant, "**rendered** [link](https://example.com)"),
	}}
	out, _ := renderRows(m, opts{}, nil, nil, 40, "", "", newMarkdown())
	got := plain(out)
	if !strings.Contains(got, "**as typed**") {
		t.Errorf("the user's message is not shown as typed:\n%s", got)
	}
	if !strings.Contains(got, "rendered link") || strings.Contains(got, "**rendered**") {
		t.Errorf("the assistant's message is not rendered:\n%s", got)
	}
	// The row's wrap to the conversation's width keeps the hyperlink.
	if !strings.Contains(out, "\x1b]8;") || !strings.Contains(out, ";https://example.com\a") {
		t.Errorf("the hyperlink is lost in the conversation: %q", out)
	}
}
