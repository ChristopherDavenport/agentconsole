// Package toolview is how a client learns to show one tool's calls: a
// renderer per tool name, which an embedder hands the client, and the
// lines a renderer returns.
//
// A renderer reads only what the record holds of a call, through
// [view.Call]: its name and arguments, its state, its progress and its
// output, and the parameters schema of the tool as the config in force
// at the call names it. It never reaches the tool or the agent. So one
// renderer draws a call streaming in, waiting on a permission, running,
// replayed from a session months old or read from a store another
// process writes, and it works for a tool that knows nothing of the
// client: an MCP server's, or any other.
//
// A renderer may always decline, and the client then shows the call as
// it shows any tool's: the name and the arguments as key=value pairs.
// Arguments still streaming, arguments of another shape (an old session,
// a tool of the same name from elsewhere) or a schema that is not the
// one the renderer was written for are all reasons to decline.
//
// Lines carry roles, not styles: the client keeps the colors, the
// width and the wrapping, so a renderer is the same for any client that
// renders [view.Model].
package toolview

import (
	"strings"

	"github.com/ChristopherDavenport/agentconsole/view"
)

// Renderer shows the calls of one tool.
//
// The client calls it on its own goroutine each time it lays the
// conversation out, which may be every frame of a spinner, so it is
// quick and has no side effects. It must not keep the call: what the
// call holds is valid for the one call. A renderer that panics is taken
// to have declined.
type Renderer interface {
	// Head is what follows the tool's name on the call's line: what the
	// call does, from its arguments alone, so it reads the same while
	// the arguments stream, under a permission and once the call has
	// ended. The client draws the name, the state and the spinner
	// around it. ok is false to show the arguments as the client does.
	Head(c view.Call) (head Line, ok bool)
	// Body is what shows under the call's line: the progress of a
	// running call, the result of one that ended, or what a call waiting
	// on a permission would do. expanded is set when the user asked to
	// see the call in full; collapsed, a body is best kept to a line or
	// two. The client's own notes (the question of a deferred call, why
	// a call was blocked or cut off, the policy's verdict) stay, and
	// expanded the raw arguments are always shown above the body. ok is
	// false to show what the client does: the progress tail of a
	// running call and the size or the whole of an output.
	Body(c view.Call, expanded bool) (body []Line, ok bool)
}

// Renderers are the renderers a client uses, by tool name as the model
// calls it, an MCP tool's prefix included. A call to a tool with no
// renderer is shown as the client shows any tool's.
type Renderers map[string]Renderer

// Role is what a span of a line is, which the client draws in its own
// way.
type Role int

const (
	// Plain is text drawn as the client draws text.
	Plain Role = iota
	// Dim is text of less weight: a count, a note, context around a
	// change.
	Dim
	// Emphasis is the part of a line the eye should find first: a path,
	// a command, a pattern.
	Emphasis
	// Added is a line or a part a change adds.
	Added
	// Removed is a line or a part a change removes.
	Removed
	// Error is a failure: a non-zero exit, a refused write.
	Error
)

// Span is a run of text with one role. Its text is one line; a client
// draws a newline in it as a space.
type Span struct {
	Text string
	Role Role
}

// Line is one line of spans.
type Line []Span

// S is a span of text with role r.
func S(r Role, text string) Span { return Span{Text: text, Role: r} }

// Text is s as lines, each one span of role r, with a trailing newline
// dropped: what a renderer returns for a block of output.
func Text(r Role, s string) []Line {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	lines := make([]Line, len(parts))
	for i, p := range parts {
		lines[i] = Line{S(r, p)}
	}
	return lines
}

// String is the line's text, its roles dropped.
func (l Line) String() string {
	var b strings.Builder
	for _, s := range l {
		b.WriteString(s.Text)
	}
	return b.String()
}
