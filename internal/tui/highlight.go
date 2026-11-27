package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// highlighter draws a line of code in the colors of its language: the
// spans a renderer marks with a syntax (toolview.Span.Syntax), a diff's
// lines and a read's. A line is lexed alone, as it is drawn alone: a
// collapsed diff or the start of a read is seldom a whole file, and a
// string or a comment that runs over lines is then colored as code on
// the lines after its first, which reads better than no color at all.
//
// What it drew is kept by line until a layout does not ask for it again,
// as the Markdown is: the conversation is laid out on every frame of the
// spinner, and a session's diffs would otherwise be lexed on each.
type highlighter struct {
	// lexers are the lexers found so far by syntax, nil for a syntax no
	// lexer takes.
	lexers      map[string]chroma.Lexer
	cache, used map[hlKey]string
}

type hlKey struct {
	syntax, text string
	dark         bool
	// bg is the background the line is drawn on, "" for the terminal's.
	bg string
}

func newHighlighter() *highlighter {
	return &highlighter{lexers: map[string]chroma.Lexer{}, cache: map[hlKey]string{}, used: map[hlKey]string{}}
}

// sweep ends a layout: what it did not draw is dropped.
func (h *highlighter) sweep() {
	if h != nil {
		h.cache, h.used = h.used, map[hlKey]string{}
	}
}

// lexer is the lexer for a syntax: a file's name (`main.go`, `Makefile`)
// or a language's (`go`), nil when none takes it. Plain text has none:
// there is nothing in it to color.
func (h *highlighter) lexer(syntax string) chroma.Lexer {
	if l, ok := h.lexers[syntax]; ok {
		return l
	}
	l := lexers.Match(syntax)
	if l == nil {
		l = lexers.Get(syntax)
	}
	if l != nil {
		if cfg := l.Config(); cfg == nil || cfg.Name == "plaintext" || cfg.Name == "fallback" {
			l = nil
		} else {
			l = chroma.Coalesce(l)
		}
	}
	// A syntax is a path's name, which a session has few of; a record
	// that names many is not let grow the map without end.
	if len(h.lexers) >= 256 {
		h.lexers = map[string]chroma.Lexer{}
	}
	h.lexers[syntax] = l
	return l
}

// draw is text, one line already cleaned, in the colors of syntax on a
// dark or a light terminal, over bg when it is not nil. ok is false when
// no lexer takes the syntax or the line does not lex, and the caller
// draws the text as it draws any.
func (h *highlighter) draw(syntax, text string, dark bool, bg color.Color) (out string, ok bool) {
	if h == nil || syntax == "" || strings.TrimSpace(text) == "" {
		return "", false
	}
	key := hlKey{syntax: syntax, text: text, dark: dark}
	if bg != nil {
		r, g, b, _ := bg.RGBA()
		key.bg = string([]byte{byte(r >> 8), byte(g >> 8), byte(b >> 8)})
	}
	if s, hit := h.used[key]; hit {
		return s, true
	}
	if s, hit := h.cache[key]; hit {
		h.used[key] = s
		return s, true
	}
	lexer := h.lexer(syntax)
	if lexer == nil {
		return "", false
	}
	// A lexer is a set of regular expressions run over text the model or
	// a file wrote: one that panics has not lexed the line.
	defer func() {
		if recover() != nil {
			out, ok = "", false
		}
	}()
	it, err := lexer.Tokenise(nil, text)
	if err != nil {
		return "", false
	}
	theme := styles.Get("github-dark")
	if !dark {
		theme = styles.Get("github")
	}
	var b strings.Builder
	for _, tok := range it.Tokens() {
		v := strings.ReplaceAll(tok.Value, "\n", "")
		if v == "" {
			continue
		}
		st := lipgloss.NewStyle()
		if bg != nil {
			st = st.Background(bg)
		}
		// The theme's colors and weight, never its background: the line
		// sits on the terminal's own or on a change's band.
		if e := theme.Get(tok.Type); e.Colour.IsSet() {
			st = st.Foreground(lipgloss.Color(e.Colour.String()))
		}
		b.WriteString(st.Render(v))
	}
	h.used[key] = b.String()
	return b.String(), true
}
