// Package cmd is the featureflip command tree.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/canopy-labs/featureflip-cli/internal/exitcode"
	"github.com/canopy-labs/featureflip-cli/internal/version"
)

// UsageError is a mistake in how the command was invoked. Exit code 2.
type UsageError struct{ Msg string }

func (e UsageError) Error() string { return e.Msg }
func (UsageError) ExitCode() int   { return exitcode.Usage }

// ConfirmError means a confirmation was declined, or was required with no one
// there to give it. Exit code 6.
type ConfirmError struct{ Msg string }

func (e ConfirmError) Error() string { return e.Msg }
func (ConfirmError) ExitCode() int   { return exitcode.Confirm }

// unexpected marks an error raised inside a command that carries no code of
// its own, so it is not mistaken for one of cobra's own parse errors.
type unexpected struct{ err error }

func (e unexpected) Error() string { return e.err.Error() }
func (e unexpected) Unwrap() error { return e.err }
func (unexpected) ExitCode() int   { return exitcode.Unexpected }

// globals are the persistent flags every command shares.
type globals struct {
	org, project string
	json, yes    bool
	noColor      bool
}

// Execute runs the CLI with args and returns the process exit code.
func Execute(ctx context.Context, args []string, env Env) int {
	g := &globals{}
	root := newRoot(env, g)
	root.SetArgs(args)
	root.SetIn(env.In)
	root.SetOut(env.Out)
	root.SetErr(env.Err)
	markCommandErrors(root)

	var err error
	if flag := unknownLeadingFlag(root, args); flag != "" {
		err = fmt.Errorf("unknown flag: %s", flag)
	} else {
		err = root.ExecuteContext(ctx)
	}
	if err == nil {
		return exitcode.OK
	}
	var coded exitcode.Coded
	if !errors.As(err, &coded) {
		// Anything that is not Coded came from cobra itself: an unknown
		// command or flag, a wrong argument count, a missing required flag.
		err = UsageError{Msg: err.Error()}
		// A parse error can stop cobra before it reaches --json; the
		// caller still asked for one JSON document on stdout.
		if !g.json {
			g.json = wantsJSON(args)
		}
	}
	renderError(env, g, err)
	return exitcode.Classify(err)
}

func newRoot(env Env, g *globals) *cobra.Command {
	root := &cobra.Command{
		Use:           "featureflip",
		Short:         "Manage Featureflip feature flags from the terminal",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	pf := root.PersistentFlags()
	pf.StringVar(&g.org, "org", "", "organization slug (default: FEATUREFLIP_ORG, the saved default, or your only org)")
	pf.StringVar(&g.project, "project", "", "project key (default: FEATUREFLIP_PROJECT, the saved default, or your only project)")
	pf.BoolVar(&g.json, "json", false, "print machine-readable JSON on stdout")
	pf.BoolVar(&g.yes, "yes", false, "confirm changes to environments that require confirmation, without prompting")
	pf.BoolVar(&g.noColor, "no-color", false, "disable colored output")

	root.AddCommand(
		newVersionCmd(env, g),
		newWhoamiCmd(env, g),
		newLoginCmd(env, g),
		newLogoutCmd(env, g),
		newProjectsCmd(env, g),
		newEnvironmentsCmd(env, g),
		newSegmentsCmd(env, g),
		newFlagsCmd(env, g),
		newGenerateCmd(env, g),
	)
	return root
}

// unknownLeadingFlag returns the first flag before the command name that the
// root doesn't define, or "". Cobra finds the command before parsing flags and
// assumes an unknown flag there takes the next word as its value, so
// `--bogus flags list` would fail as `unknown command "list"` instead of
// naming --bogus. Only the root's (persistent) flags can come first; a
// subcommand's own flags are left to cobra, which accepts them anywhere.
func unknownLeadingFlag(root *cobra.Command, args []string) string {
	flags := root.PersistentFlags()
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" || a == "-" || !strings.HasPrefix(a, "-") {
			return "" // the command name, or no flags left before it
		}
		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if name == "help" || name == "h" {
			continue
		}
		f := flags.Lookup(name)
		if !strings.HasPrefix(a, "--") {
			f = flags.ShorthandLookup(name[:1])
		}
		if f == nil {
			if !hasValue {
				return a
			}
			return a[:strings.Index(a, "=")]
		}
		if !hasValue && f.NoOptDefVal == "" {
			i++ // the flag's value is the next word
		}
	}
	return ""
}

// wantsJSON reports whether args turn on --json, for when parsing failed
// before cobra got to it. Arguments after a "--" terminator are not flags.
func wantsJSON(args []string) bool {
	on := false
	for _, a := range args {
		switch {
		case a == "--":
			return on
		case a == "--json":
			on = true
		case strings.HasPrefix(a, "--json="):
			v, err := strconv.ParseBool(strings.TrimPrefix(a, "--json="))
			on = err == nil && v
		}
	}
	return on
}

// markCommandErrors wraps every RunE so an uncoded error a command returns is
// classified as unexpected (exit 1) rather than as cobra's usage errors.
func markCommandErrors(c *cobra.Command) {
	if c.RunE != nil {
		inner := c.RunE
		c.RunE = func(cmd *cobra.Command, args []string) error {
			err := inner(cmd, args)
			var coded exitcode.Coded
			if err != nil && !errors.As(err, &coded) {
				return unexpected{err}
			}
			return err
		}
	}
	for _, sub := range c.Commands() {
		markCommandErrors(sub)
	}
}

func newVersionCmd(env Env, g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			platform := runtime.GOOS + "/" + runtime.GOARCH
			if g.json {
				return printJSON(env.Out, map[string]string{
					"version":  version.Version,
					"commit":   version.Commit,
					"go":       runtime.Version(),
					"platform": platform,
				})
			}
			_, err := fmt.Fprintf(env.Out, "featureflip %s (commit %s, %s, %s)\n",
				version.Version, version.Commit, runtime.Version(), platform)
			return err
		},
	}
}

// groupCmd is a command that only holds subcommands. Cobra treats an unknown
// word after a non-runnable command as a request for help (exit 0), which
// would let `featureflip flags of new-checkout` look like it worked.
func groupCmd(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return UsageError{fmt.Sprintf("unknown command %q for %q", args[0], cmd.CommandPath())}
			}
			return cmd.Help()
		},
	}
}

// checkKey refuses keys that can't name a resource in a URL path: empty (an
// unset shell variable, say), "." and "..", which would address a different
// route. Called before any request so the mistake reads as a usage error.
func checkKey(what, key string) error {
	switch key {
	case "":
		return UsageError{fmt.Sprintf("%s is empty", what)}
	case ".", "..":
		return UsageError{fmt.Sprintf("%s can't be %q", what, key)}
	}
	return nil
}
