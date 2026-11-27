package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/ChristopherDavenport/agentconsole/toolview"
	"github.com/ChristopherDavenport/agentconsole/view"
)

// colors counts the distinct foreground colors a drawn string sets.
func colors(s string) int {
	seen := map[string]bool{}
	for _, part := range strings.Split(s, "\x1b[")[1:] {
		code, _, _ := strings.Cut(part, "m")
		for _, c := range strings.Split(code, ";48;") {
			if strings.HasPrefix(c, "38;") {
				seen[c] = true
			}
		}
	}
	return len(seen)
}

// A span that is code is drawn in its language's colors, by a file's
// name or a language's, and its text is what it was.
func TestCodeIsDrawnInItsLanguagesColors(t *testing.T) {
	h := newHighlighter()
	src := `func Hello(name string) (string, error) { return "hi", nil } // greets`
	for _, syntax := range []string{"greet.go", "go", "cmd/greet/main.go"} {
		got, ok := h.draw(syntax, src, true, nil)
		if !ok || ansi.Strip(got) != src {
			t.Fatalf("%s: drew %q, ok %v", syntax, ansi.Strip(got), ok)
		}
		if n := colors(got); n < 3 {
			t.Errorf("%s: %d colors in a line with a keyword, a string and a comment:\n%q", syntax, n, got)
		}
	}
	dark, _ := h.draw("a.go", src, true, nil)
	light, _ := h.draw("a.go", src, false, nil)
	if dark == light {
		t.Error("the colors are the same on a dark and a light terminal")
	}
	// What no lexer takes, plain text and an empty line are the caller's
	// to draw; nothing panics on a nil highlighter.
	for _, c := range [][2]string{{"notes.unknownext", "x := 1"}, {"notes.txt", "some words"}, {"", "x := 1"}, {"a.go", "   "}} {
		if got, ok := h.draw(c[0], c[1], true, nil); ok {
			t.Errorf("syntax %q, text %q: drew %q", c[0], c[1], got)
		}
	}
	var none *highlighter
	if _, ok := none.draw("a.go", src, true, nil); ok {
		t.Error("a nil highlighter drew")
	}
	none.sweep()
}

// A line is lexed once and kept while layouts ask for it, and dropped
// by the first that does not.
func TestAHighlightedLineIsKeptWhileItIsDrawn(t *testing.T) {
	h := newHighlighter()
	h.draw("a.go", "x := 1", true, nil)
	h.sweep()
	if len(h.cache) != 1 {
		t.Fatalf("after a layout the cache holds %d lines, want 1", len(h.cache))
	}
	h.draw("a.go", "x := 1", true, nil)
	h.sweep()
	if len(h.cache) != 1 {
		t.Errorf("a line drawn again was not kept: %d", len(h.cache))
	}
	h.sweep()
	if len(h.cache) != 0 {
		t.Errorf("a line no layout drew is still kept: %d", len(h.cache))
	}
}

// codeChange draws a call as a kept line, a removed one and an added
// one of Go, each marked as code.
type codeChange struct{}

func (codeChange) Head(view.Call) (toolview.Line, bool) {
	return toolview.Line{toolview.S(toolview.Emphasis, "a.go")}, true
}

func (codeChange) Body(view.Call, bool) ([]toolview.Line, bool) {
	line := func(r toolview.Role, mark, code string) toolview.Line {
		return toolview.Line{toolview.S(r, mark), toolview.Code(r, "a.go", code)}
	}
	return []toolview.Line{
		line(toolview.Dim, "  ", `func old() string {`),
		line(toolview.Removed, "- ", `	return "old" // was`),
		line(toolview.Added, "+ ", `	return "new" // now`),
	}, true
}
func (codeChange) Loud() bool { return true }

// A change's lines of code keep their band and their text, with the
// language's colors over the band; context is colored too; and with 16
// colors, where there is no band, a changed line is green or red alone.
func TestCodeInAChangeIsColoredOverItsBand(t *testing.T) {
	tools := toolview.Renderers{"edit": codeChange{}}
	m := view.Model{Rows: []view.Row{editRow("c1", "r1")}}
	draw := func(p colorprofile.Profile) string {
		md := newMarkdown()
		md.profile = p
		out, _ := renderRows(m, opts{}, nil, tools, 60, "", "", md, asks{}, outlineState{loud: loudOf(tools)})
		return out
	}
	out := draw(colorprofile.TrueColor)
	if got := plain(out); !strings.Contains(got, "○ edit a.go\n    func old() string {\n  - \treturn \"old\" // was\n  + \treturn \"new\" // now") &&
		!strings.Contains(got, "  -     return \"old\" // was") {
		t.Fatalf("the text changed:\n%q", got)
	}
	var kept, gone, added string
	for _, l := range strings.Split(out, "\n") {
		switch p := ansi.Strip(l); {
		case strings.Contains(p, "func old"):
			kept = l
		case strings.Contains(p, "was"):
			gone = l
		case strings.Contains(p, "now"):
			added = l
		}
	}
	if colors(kept) < 2 {
		t.Errorf("context is not colored as code: %q", kept)
	}
	for name, l := range map[string]string{"removed": gone, "added": added} {
		if colors(l) < 2 {
			t.Errorf("the %s line is not colored as code: %q", name, l)
		}
		if ansi.StringWidth(l) != 60 || !strings.Contains(l, "48;2;") {
			t.Errorf("the %s line is not a band to the width: %d wide, %q", name, ansi.StringWidth(l), l)
		}
		// Every colored run sits on the band: none resets it to the
		// terminal's background before the line's end.
		for _, part := range strings.Split(l, "\x1b[")[1:] {
			code, text, _ := strings.Cut(part, "m")
			if text != "" && strings.TrimSpace(text) != "" && !strings.Contains(code, "48;2;") {
				t.Errorf("the %s line has text off its band: %q in %q", name, text, l)
			}
		}
	}
	// 16 colors: no band, and a changed line is its role's color alone.
	few := draw(colorprofile.ANSI)
	for _, l := range strings.Split(few, "\n") {
		if p := ansi.Strip(l); strings.Contains(p, "was") || strings.Contains(p, "now") {
			if colors(l) != 0 || strings.Contains(l, "48;") {
				t.Errorf("16 colors: a changed line is highlighted or banded: %q", l)
			}
		}
	}
}
