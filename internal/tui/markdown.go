package tui

import (
	"regexp"
	"strings"

	"charm.land/glamour/v2"
	gansi "charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// tabWidth is how many columns a tab stands for: CommonMark's tab stop.
const tabWidth = 4

// markdown renders the assistant's messages with glamour: CommonMark with
// GitHub's tables, task lists, strikethrough and bare links, code blocks
// highlighted, wrapped to a width.
//
// What it rendered is kept by source and width until a layout does not
// ask for it again, so a frame of the spinner does not parse the
// conversation again, and a message streaming in leaves only its last
// state behind. A glamour renderer is built once per width.
type markdown struct {
	dark      bool
	renderers map[int]*glamour.TermRenderer
	// cache is what the last layout rendered; used what this one has so
	// far, which replaces it at the layout's end.
	cache, used map[mdKey]string
}

type mdKey struct {
	src   string
	width int
}

func newMarkdown() *markdown {
	return &markdown{
		dark:      true,
		renderers: map[int]*glamour.TermRenderer{},
		cache:     map[mdKey]string{},
		used:      map[mdKey]string{},
	}
}

// setDark says whether the terminal's background is dark, which picks
// glamour's dark or light style.
func (r *markdown) setDark(dark bool) {
	if r.dark == dark {
		return
	}
	r.dark = dark
	r.renderers = map[int]*glamour.TermRenderer{}
	r.cache, r.used = map[mdKey]string{}, map[mdKey]string{}
}

// render is src rendered to lines width columns wide. Source glamour
// cannot render is shown as it is.
func (r *markdown) render(src string, width int) string {
	k := mdKey{src, width}
	if s, ok := r.used[k]; ok {
		return s
	}
	s, ok := r.cache[k]
	if !ok {
		s = src
		if tr, err := r.renderer(width); err == nil {
			if out, err := tr.Render(expandTabs(src)); err == nil {
				s = trimBlankLines(out)
			}
		}
	}
	r.used[k] = s
	return s
}

// sweep ends a layout: what it did not render is dropped.
func (r *markdown) sweep() {
	r.cache, r.used = r.used, map[mdKey]string{}
}

// renderer is glamour's renderer for width. A resize asks for another
// width; the ones before it are dropped once there are a few.
func (r *markdown) renderer(width int) (*glamour.TermRenderer, error) {
	if tr, ok := r.renderers[width]; ok {
		return tr, nil
	}
	tr, err := glamour.NewTermRenderer(glamour.WithStyles(mdStyle(r.dark)), glamour.WithWordWrap(width))
	if err != nil {
		return nil, err
	}
	if len(r.renderers) >= 4 {
		r.renderers = map[int]*glamour.TermRenderer{}
	}
	r.renderers[width] = tr
	return tr, nil
}

// mdStyle is glamour's dark or light style, fitted to the conversation:
// no margin or blank lines around the document, which sits under its
// row's label as any row does, in the terminal's own text color; no
// "##" before a heading, the markup glamour otherwise keeps (the first
// level is still a bar, the second underlined); code in a gray a shade
// off the text rather than glamour's red; and a quote's bar one column
// wide.
func mdStyle(dark bool) gansi.StyleConfig {
	s, code := styles.DarkStyleConfig, "250"
	if !dark {
		s, code = styles.LightStyleConfig, "238"
	}
	zero := uint(0)
	s.Document.Margin = &zero
	s.Document.BlockPrefix, s.Document.BlockSuffix = "", ""
	s.Document.Color = nil
	for _, h := range []*gansi.StyleBlock{&s.H2, &s.H3, &s.H4, &s.H5, &s.H6} {
		h.Prefix = ""
	}
	underline := true
	s.H2.Underline = &underline
	s.Code.Color = &code
	// glamour sets aside one column for each level of quote and draws
	// the two of "│ " in it, so a quote's lines ran a column over and
	// broke; a bar at the left of one column leaves the gap in the cell.
	bar := "▎"
	s.BlockQuote.IndentToken = &bar
	return s
}

// trimBlankLines drops the blank lines glamour leaves before and after
// what it rendered, and the spaces it pads the last line out to the
// width with, where a streaming row's cursor goes.
func trimBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	blank := func(l string) bool { return strings.TrimSpace(ansi.Strip(l)) == "" }
	for len(lines) > 0 && blank(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && blank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	if n := len(lines); n > 0 {
		lines[n-1] = trimPadding(lines[n-1])
	}
	return strings.Join(lines, "\n")
}

// trailingPad is a line's end of spaces and SGR sequences.
var trailingPad = regexp.MustCompile(`(?:\x1b\[[0-9;]*m| )+$`)

// trimPadding drops the spaces at the end of a styled line, and closes
// any style the cut left open.
func trimPadding(l string) string {
	end := trailingPad.FindString(l)
	if !strings.Contains(end, " ") {
		return l
	}
	l = strings.TrimSuffix(l, end)
	if strings.Contains(end, "\x1b[") {
		l += "\x1b[m"
	}
	return l
}

// expandTabs replaces each tab with the spaces to the next tab stop, line
// by line: glamour keeps a code block's tabs, which a terminal's cells
// cannot measure.
func expandTabs(s string) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	col := 0
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		switch c := g.Str(); c {
		case "\t":
			n := tabWidth - col%tabWidth
			b.WriteString(strings.Repeat(" ", n))
			col += n
		case "\n", "\r\n":
			b.WriteString(c)
			col = 0
		default:
			b.WriteString(c)
			col += ansi.StringWidth(c)
		}
	}
	return b.String()
}
