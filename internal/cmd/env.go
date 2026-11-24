package cmd

import (
	"io"
	"net/http"
	"os"
	"time"

	"github.com/canopy-labs/featureflip-cli/internal/term"
)

// Env is everything a command touches outside its own arguments. Tests build
// one with buffers, a fake terminal and a temp config dir.
type Env struct {
	In       io.Reader
	Out, Err io.Writer
	Getenv   func(string) string
	Terminal term.Terminal
	// StdoutTTY reports whether stdout is a terminal: color goes only there,
	// whatever stdin and stderr are.
	StdoutTTY  bool
	ConfigDir  func() (string, error)
	HTTPClient *http.Client
	// Sleep replaces every wait between retries; tests set it to record them.
	// The real process leaves it nil, so each client waits on its context and
	// Ctrl-C ends a rate-limit wait at once.
	Sleep func(time.Duration)
}

// DefaultEnv wires Env to the real process.
func DefaultEnv() Env {
	return Env{
		In:         os.Stdin,
		Out:        os.Stdout,
		Err:        os.Stderr,
		Getenv:     os.Getenv,
		Terminal:   term.NewOS(),
		StdoutTTY:  term.IsTerminal(os.Stdout),
		ConfigDir:  os.UserConfigDir,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}
