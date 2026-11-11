// Package term is the CLI's view of the user's terminal: whether a person is
// there to answer, and how to ask them something.
package term

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	xterm "golang.org/x/term"
)

// Terminal asks the user questions. Commands never read os.Stdin directly for
// answers, so tests can script the conversation.
type Terminal interface {
	// Interactive reports whether someone can answer a prompt: true only when
	// stdin and stderr are both terminals, so a CI job that happens to
	// allocate a TTY for one stream still has to pass --yes.
	Interactive() bool
	Prompt(msg string) (string, error)
	PromptSecret(msg string) (string, error)
}

// OS is the real terminal on stdin/stderr.
type OS struct {
	reader *bufio.Reader
}

func NewOS() *OS { return &OS{reader: bufio.NewReader(os.Stdin)} }

func (t *OS) Interactive() bool {
	return xterm.IsTerminal(int(os.Stdin.Fd())) && xterm.IsTerminal(int(os.Stderr.Fd()))
}

func (t *OS) Prompt(msg string) (string, error) {
	fmt.Fprint(os.Stderr, msg)
	line, err := t.reader.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (t *OS) PromptSecret(msg string) (string, error) {
	fmt.Fprint(os.Stderr, msg)
	b, err := xterm.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// IsTerminal reports whether f is a terminal.
func IsTerminal(f *os.File) bool { return xterm.IsTerminal(int(f.Fd())) }
