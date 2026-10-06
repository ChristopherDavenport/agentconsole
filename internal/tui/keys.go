package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// The keys screen lists every key the client takes. It is opened with
// ctrl+/ or F1: a terminal that reports keys unambiguously (the kitty
// keyboard protocol, which bubbletea asks for) names it ctrl+/; the rest
// send it as the unit separator, which bubbletea names ctrl+_, and most
// send ctrl+? (ctrl+shift+/) the same way; the ones that send it as DEL
// cannot be told from backspace, so F1 opens it too.

// keyGroup is a heading and its keys, each a key and what it does.
type keyGroup struct {
	title string
	keys  [][2]string
}

var keyGroups = []keyGroup{
	{"Conversation", [][2]string{
		{"Enter", "send a prompt or steer the run; Shift-Enter, Alt-Enter, Ctrl-J: new line"},
		{"Ctrl-C", "copy the selection when one is drawn; otherwise abort the run, quit when idle (a second one quits at once)"},
		{"PgUp PgDn", "scroll a page"},
		{"Ctrl-Up Ctrl-Down", "scroll a line"},
		{"Ctrl-Home Ctrl-End", "scroll to the top or the bottom"},
		{"mouse wheel", "scroll"},
		{"mouse drag", "select text; it stays on the text as it scrolls, until the next key or press"},
		{"click the input", "put the cursor there, and leave the selected row"},
		{"Ctrl-R", "show or hide reasoning: the selected row's, or every row's"},
		{"Ctrl-O", "show tool arguments and output in full: the selected row's, or every row's"},
		{"Ctrl-/  F1", "this list"},
	}},
	{"Rows", [][2]string{
		{"Ctrl-P Ctrl-N", "move the row cursor up or down"},
		{"click", "select the row; click it again to expand or collapse it"},
		{"Tab", "the pane: the row's detail, the session, none"},
		{"Ctrl-B", "continue from the selected row"},
		{"Esc", "clear the cursor and close the pane"},
	}},
	{"Permissions", [][2]string{
		{"y", "approve the call"},
		{"n", "refuse it; type an optional reason, Enter refuses, Esc goes back"},
	}},
	{"Tree (Ctrl-T)", [][2]string{
		{"Up Down  k j", "select"},
		{"Enter", "view the branch, or open the session, read only"},
		{"click", "select the item; click it again to open it"},
		{"c", "continue from the branch"},
		{"Esc  q  Ctrl-T", "back to the conversation"},
	}},
	{"Read-only view", [][2]string{
		{"Esc", "back to the live session"},
		{"c", "continue from the cursor's row, or the branch's tip"},
	}},
}

// renderKeys is the keys screen's content.
func renderKeys() string {
	width := 0
	for _, g := range keyGroups {
		for _, k := range g.keys {
			width = max(width, len(k[0]))
		}
	}
	var b strings.Builder
	for i, g := range keyGroups {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(assistantStyle.Render(g.title) + "\n")
		for _, k := range g.keys {
			b.WriteString("  " + toolStyle.Render(k[0]) + strings.Repeat(" ", width-len(k[0])+2) + k[1] + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// isKeysKey is whether msg opens or closes the keys screen.
func isKeysKey(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "ctrl+/", "ctrl+_", "f1":
		return true
	}
	return false
}

// isNewlineKey is whether msg breaks the input's line rather than sending
// it. Only a terminal that speaks the kitty keyboard protocol (which
// bubbletea asks for) reports Shift+Enter apart from Enter; the rest send
// both as a carriage return. Alt+Enter (ESC CR) and Ctrl+J (a line feed)
// reach the client from every terminal, so they break the line too.
func isNewlineKey(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "shift+enter", "alt+enter", "ctrl+j":
		return true
	}
	return false
}

// keysKey handles a key on the keys screen: Esc or q goes back.
func (m *Model) keysKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.screen = screenConversation
		m.relayout()
	}
	return m, nil
}
