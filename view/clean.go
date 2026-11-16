package view

import (
	"strings"
	"unicode/utf8"
)

// Clean is text the client did not author, made safe to draw on a
// terminal: what a model, a tool, a provider, an MCP server or a peer
// wrote, and what the record holds of it. It removes what would act on
// the terminal rather than show on it: the C0 controls other than
// newline and tab (ESC, carriage return, backspace, bell, ...), DEL, the
// C1 controls (U+0080-U+009F), and the bidirectional formatting
// characters (U+061C, U+200E-U+200F, U+202A-U+202E, U+2066-U+2069),
// which make text read as something other than it is.
//
// A removed character is dropped, not shown in another form. An escape
// sequence loses its ESC and shows as the harmless text it would have
// been ("[2J", "]0;title"); removing the one character, rather than
// parsing the sequence, keeps a sequence cut in two by streaming deltas
// from working. Everything else is kept as it is, newlines, tabs and
// every printable character of any script included; a byte that is not
// UTF-8 (a raw 0x9b, which a terminal set for 8-bit controls reads as
// CSI) becomes U+FFFD. A client cleans each untrusted string before it
// styles it or renders it as Markdown, so its own styling still reaches
// the terminal.
func Clean(s string) string {
	i := strings.IndexFunc(s, controls)
	switch {
	case i < 0 && utf8.ValidString(s):
		return s
	case i < 0 || !utf8.ValidString(s[:i]):
		i = 0
	}
	var b strings.Builder
	b.Grow(len(s))
	b.WriteString(s[:i])
	for _, r := range s[i:] {
		// An invalid byte is ranged over as utf8.RuneError, which
		// WriteRune writes as U+FFFD.
		if !controls(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// controls is whether Clean removes r.
func controls(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		return true
	case r == 0x061c, r == 0x200e, r == 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// CleanLine is Clean for text drawn on one row: a name, a label, an ID,
// an error, a status segment, the line a panel asks about. Each newline
// and tab Clean keeps becomes a space, so the text cannot add a row that
// reads as the client's own, or take rows the layout did not give it.
func CleanLine(s string) string {
	s = Clean(s)
	if !strings.ContainsAny(s, "\n\t") {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		return r
	}, s)
}
