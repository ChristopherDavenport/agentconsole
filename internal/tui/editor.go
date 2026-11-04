package tui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// editedMsg is what the editor left in the file, or why there is none.
type editedMsg struct {
	text string
	err  error
}

// errNoEditor is Ctrl-G with neither $VISUAL nor $EDITOR set.
var errNoEditor = errors.New("no editor: set $VISUAL or $EDITOR")

// editInput is Ctrl-G: the input is written to a private file (mode
// 0600, in a directory of its own) and the program suspends for the
// user's editor, $VISUAL or else $EDITOR, split as a shell splits it.
// What the editor saved is put back in the input, not sent. The file is
// removed afterwards, whatever the editor did, since a prompt may hold
// a pasted secret. It is refused while a question or a permission is
// being answered, whose keys are y and n.
func (m *Model) editInput() tea.Cmd {
	if _, _, ok := m.question(); ok {
		return m.noEdit(errors.New("not while a question is asked"))
	}
	if _, _, ok := m.pending(); ok {
		return m.noEdit(errors.New("not while a permission is asked"))
	}
	if !m.typing() {
		return nil
	}
	argv, err := editorArgs()
	if err != nil {
		return m.noEdit(err)
	}
	dir, err := os.MkdirTemp("", "agentconsole-edit-")
	if err != nil {
		return m.noEdit(err)
	}
	file := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(file, []byte(m.in.Value()), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return m.noEdit(err)
	}
	c := exec.Command(argv[0], append(argv[1:], file)...)
	return m.exec(c, func(err error) tea.Msg {
		defer os.RemoveAll(dir)
		if err != nil {
			return editedMsg{err: err}
		}
		data, err := os.ReadFile(file)
		return editedMsg{text: string(data), err: err}
	})
}

func (m *Model) noEdit(err error) tea.Cmd {
	return func() tea.Msg { return editedMsg{err: err} }
}

// edited puts what the editor saved in the input, less the newline an
// editor ends a file with; on a failure the input stays as it was and
// the status line says why.
func (m *Model) edited(msg editedMsg) {
	if msg.err != nil {
		m.err = "editor: " + msg.err.Error()
	} else {
		m.err = ""
		m.in.SetValue(strings.TrimSuffix(strings.TrimSuffix(msg.text, "\n"), "\r"))
	}
	m.relayout()
}

// editorArgs is $VISUAL, or $EDITOR when it is unset or empty, split
// into words as a shell would: blanks separate them, and single quotes,
// double quotes and backslashes quote.
func editorArgs() ([]string, error) {
	v := os.Getenv("VISUAL")
	if strings.TrimSpace(v) == "" {
		v = os.Getenv("EDITOR")
	}
	if strings.TrimSpace(v) == "" {
		return nil, errNoEditor
	}
	return shellWords(v)
}

func shellWords(s string) ([]string, error) {
	var words []string
	var w strings.Builder
	inWord := false
	var quote rune
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			w.WriteRune(r)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				w.WriteRune(r)
			}
		case quote == '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			default:
				w.WriteRune(r)
			}
		case r == '\\':
			escaped, inWord = true, true
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case unicode.IsSpace(r):
			if inWord {
				words = append(words, w.String())
				w.Reset()
				inWord = false
			}
		default:
			w.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 || escaped {
		return nil, fmt.Errorf("cannot split the editor %q: an unclosed quote", s)
	}
	if inWord {
		words = append(words, w.String())
	}
	return words, nil
}
