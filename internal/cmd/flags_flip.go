package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/canopy-labs/featureflip-cli/internal/api"
)

// newFlipCmd builds `flags on` and `flags off`. They set an explicit state
// rather than toggling, so running one twice is harmless.
func newFlipCmd(env Env, g *globals, on bool) *cobra.Command {
	word := stateWord(on)
	var envKey, reason string
	cmd := &cobra.Command{
		Use:   word + " <flag> --env <environment>",
		Short: fmt.Sprintf("Turn a flag %s in one environment", word),
		Long: fmt.Sprintf("Turns a flag %s in one environment. Nothing is sent if it is already %s.\n\n"+
			"An environment that requires a change reason needs --reason. One that requires\n"+
			"confirmation asks you to type its key, or needs --yes when no one is at a terminal.", word, word),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if err := checkKey("flag key", key); err != nil {
				return err
			}
			if err := checkKey("--env", envKey); err != nil {
				return err
			}
			s, err := newSession(env, g)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			org, project, err := s.Scope(ctx)
			if err != nil {
				return err
			}

			var environment api.Environment
			if err := s.API.Do(ctx, "GET", projectPath(org, project, "environments", envKey), api.Request{}, &environment); err != nil {
				return err
			}
			var current api.FlagEnvConfig
			cfgPath := projectPath(org, project, "flags", key, "environments", envKey)
			if err := s.API.Do(ctx, "GET", cfgPath, api.Request{}, &current); err != nil {
				return err
			}

			if current.IsEnabled == on {
				if g.json {
					return printJSON(env.Out, flipResult(key, envKey, current.IsEnabled, on))
				}
				fmt.Fprintf(env.Out, "%s is already %s in %s\n", key, word, envKey)
				return nil
			}

			if environment.RequireChangeReason && strings.TrimSpace(reason) == "" {
				return UsageError{fmt.Sprintf(`%s requires a change reason; pass --reason "<why>"`, envKey)}
			}
			if environment.RequireConfirmation && !g.yes {
				if err := confirm(env, envKey, fmt.Sprintf(`Type "%s" to turn %s %s: `, envKey, key, word)); err != nil {
					return err
				}
			}

			enabled := on
			err = s.API.Do(ctx, "POST", append(cfgPath, "toggle"),
				api.Request{Body: api.ToggleRequest{Enabled: &enabled}, Reason: reason, Replayable: true}, nil)
			if err != nil {
				return withReasonHint(err)
			}

			if g.json {
				return printJSON(env.Out, flipResult(key, envKey, current.IsEnabled, on))
			}
			fmt.Fprintf(env.Out, "%s: %s → %s in %s\n", key, stateWord(current.IsEnabled), word, envKey)
			return nil
		},
	}
	cmd.Flags().StringVar(&envKey, "env", "", "environment key (required)")
	cmd.Flags().StringVar(&reason, "reason", "", "why you are making the change (required by some environments)")
	cmd.MarkFlagRequired("env")
	return cmd
}

func stateWord(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func flipResult(flag, envKey string, previous, current bool) map[string]any {
	return map[string]any{
		"flag":        flag,
		"environment": envKey,
		"previous":    previous,
		"current":     current,
		"changed":     previous != current,
	}
}

// confirm asks the user to type expected. With no one at a terminal it
// refuses rather than guessing, naming --yes as the way through.
func confirm(env Env, expected, prompt string) error {
	if env.Terminal == nil || !env.Terminal.Interactive() {
		return ConfirmError{fmt.Sprintf("%s requires confirmation; re-run with --yes to confirm non-interactively", expected)}
	}
	answer, err := env.Terminal.Prompt(prompt)
	if err != nil || strings.TrimSpace(answer) != expected {
		return ConfirmError{"confirmation did not match; nothing changed"}
	}
	return nil
}

// withReasonHint adds the --reason hint when the server, rather than our own
// pre-check, says a reason is required (the setting changed since we read it).
func withReasonHint(err error) error {
	var apiErr *api.Error
	if errors.As(err, &apiErr) && apiErr.Code == "change_reason_required" &&
		!strings.Contains(apiErr.Message, "--reason") {
		apiErr.Message = strings.TrimRight(apiErr.Message, ". ") + `; pass --reason "<why>"`
	}
	return err
}
