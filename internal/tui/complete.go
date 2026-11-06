package tui

// Completion of the input, from the host.
//
// The host gives the completions (WithCompletion): its slash commands,
// the paths of a workspace that may be elsewhere. The client gives a word
// no meaning of its own. As the word that ends at the input's cursor
// changes, the host is asked for that word, off the program's goroutine
// and under a short timeout; an answer to an older word is dropped. A
// list with anything in it opens over the input: Up and Down choose,
// Tab puts the chosen completion in place of the word, Esc closes the
// list until the word changes. With no list open, Tab cycles the pane as
// before. What is put in the input is text: the client sends what is
// typed, an @path included, and reads no file.

import (
	"context"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// Completing is the input as the host is asked to complete it.
type Completing struct {
	// Before is the input up to the cursor, and Word its end: the text
	// back to the last space or line break, which a completion replaces.
	Before string
	Word   string
	// Running is whether a run goes.
	Running bool
}

// Completion is one completion the host offers: Text replaces the word,
// and Help, which may be empty, is shown beside it.
type Completion struct {
	Text string
	Help string
}

// completeTimeout bounds the host's answer to one word.
const completeTimeout = time.Second

// completeRows is the most completions shown at once.
const completeRows = 8

// completer is the completion state: the host's function, the word last
// asked about and the request's number, and the list open for it.
type completer struct {
	fn    func(context.Context, Completing) []Completion
	word  string
	seq   int
	items []Completion
	at    int
}

// completedMsg is the host's answer for request seq.
type completedMsg struct {
	seq   int
	items []Completion
}

// WithCompletion sets the host's completion of the input's word at the
// cursor. fn runs off the program's goroutine, under a context that ends
// after completeTimeout, and returns nothing for a word it does not
// complete.
func WithCompletion(fn func(context.Context, Completing) []Completion) Option {
	return func(m *Model) { m.comp.fn = fn }
}

// completing is the input as it is now, and whether it can be completed:
// the conversation's input is being typed in, not a refusal's reason or
// a note.
func (m *Model) completing() (Completing, bool) {
	if m.comp.fn == nil || !m.typing() || !m.in.Focused() || m.refusing != "" || m.refusingQ || m.notingQ {
		return Completing{}, false
	}
	lines := strings.Split(m.in.Value(), "\n")
	row := min(m.in.Line(), len(lines)-1)
	line := []rune(lines[row])
	col := min(m.in.Column(), len(line))
	start := col
	for start > 0 && !unicode.IsSpace(line[start-1]) {
		start--
	}
	before := strings.Join(append(lines[:row:row], string(line[:col])), "\n")
	return Completing{Before: before, Word: string(line[start:col]), Running: m.running()}, true
}

// askCompletion asks the host about the word at the cursor when it is not
// the one last asked about, and closes the list when there is none.
func (m *Model) askCompletion() tea.Cmd {
	c, ok := m.completing()
	if !ok || c.Word == "" {
		m.closeCompletion(c.Word)
		return nil
	}
	if c.Word == m.comp.word {
		return nil
	}
	// The list was for another word: it goes until the host answers for
	// this one.
	m.comp.word, m.comp.items, m.comp.at = c.Word, nil, 0
	m.comp.seq++
	seq, fn, ctx := m.comp.seq, m.comp.fn, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, completeTimeout)
		defer cancel()
		return completedMsg{seq: seq, items: fn(ctx, c)}
	}
}

// completed takes the host's answer, if it is for the word asked about
// last.
func (m *Model) completed(msg completedMsg) {
	if msg.seq != m.comp.seq {
		return
	}
	m.comp.items, m.comp.at = msg.items, 0
	m.relayout()
}

// closeCompletion closes the list and makes word the one last asked
// about, so the list stays closed until the word changes.
func (m *Model) closeCompletion(word string) {
	open := len(m.comp.items) > 0
	m.comp.word, m.comp.items, m.comp.at = word, nil, 0
	m.comp.seq++
	if open {
		m.relayout()
	}
}

// listOpen reports whether a completion list is open and can be used:
// it has something in it, for the word at the cursor now (a history
// walk or a send changes the word without asking).
func (m *Model) listOpen() bool {
	if len(m.comp.items) == 0 {
		return false
	}
	c, ok := m.completing()
	return ok && c.Word == m.comp.word
}

// completionKey handles a key while the list is open: Tab takes the
// chosen completion, Up and Down choose, Esc closes the list. It reports
// whether it took the key.
func (m *Model) completionKey(msg tea.KeyPressMsg) bool {
	if !m.listOpen() {
		return false
	}
	switch msg.String() {
	case "tab":
		m.accept(m.comp.items[m.comp.at])
	case "up":
		m.comp.at = (m.comp.at + len(m.comp.items) - 1) % len(m.comp.items)
	case "down":
		m.comp.at = (m.comp.at + 1) % len(m.comp.items)
	case "esc":
		m.closeCompletion(m.comp.word)
	default:
		return false
	}
	m.relayout()
	return true
}

// accept puts c in place of the word at the cursor. The list closes and
// stays closed for the completed word, so a word completed in full is
// not offered again; typing on (after a directory, say) asks again.
func (m *Model) accept(c Completion) {
	cur, _ := m.completing()
	for range []rune(cur.Word) {
		m.in, _ = m.in.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	m.in.InsertString(c.Text)
	now, _ := m.completing()
	m.closeCompletion(now.Word)
}

// completionLines is the open list, over the input: at most completeRows
// rows around the chosen one, the chosen one marked.
func (m *Model) completionLines() []string {
	if !m.listOpen() {
		return nil
	}
	items := m.comp.items
	from := 0
	if len(items) > completeRows {
		from = min(max(m.comp.at-completeRows/2, 0), len(items)-completeRows)
	}
	width := 0
	for _, c := range items[from:min(from+completeRows, len(items))] {
		width = max(width, len([]rune(c.Text)))
	}
	var lines []string
	for i := from; i < len(items) && i < from+completeRows; i++ {
		c := items[i]
		line := "  " + c.Text
		if c.Help != "" {
			line += strings.Repeat(" ", width-len([]rune(c.Text))+3) + c.Help
		}
		if i == m.comp.at {
			lines = append(lines, toolStyle.Render(truncate("▸"+line[1:], m.width)))
		} else {
			lines = append(lines, dimStyle.Render(truncate(line, m.width)))
		}
	}
	if len(items) > completeRows {
		lines = append(lines, dimStyle.Render(truncate("  "+itoa(m.comp.at+1)+"/"+itoa(len(items))+", tab: complete, esc: close", m.width)))
	}
	return lines
}
