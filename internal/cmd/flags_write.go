package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/canopy-labs/featureflip-cli/internal/api"
)

var flagTypes = map[string]string{"boolean": "Boolean", "string": "String", "number": "Number", "json": "Json"}

func newFlagsCreateCmd(env Env, g *globals) *cobra.Command {
	var (
		name, typ, description string
		variations, tags       []string
		clientSide             bool
	)
	cmd := &cobra.Command{
		Use:   "create <flag>",
		Short: "Create a flag (it starts off in every environment)",
		Long: "Creates a flag. Boolean by default; other types need at least two\n" +
			"--variation key=value. The flag starts off in every environment.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if err := checkKey("flag key", key); err != nil {
				return err
			}
			wireType, ok := flagTypes[strings.ToLower(typ)]
			if !ok {
				return UsageError{fmt.Sprintf("unknown --type %q; use boolean, string, number or json", typ)}
			}
			req := api.CreateFlagRequest{Key: key, Name: name, Type: wireType, Tags: tags, ClientSideVisible: clientSide}
			if req.Name == "" {
				req.Name = key
			}
			if description != "" {
				req.Description = &description
			}
			if wireType == "Boolean" && len(variations) > 0 {
				return UsageError{"boolean flags get true/false variations automatically; drop --variation or pass --type"}
			}
			if wireType != "Boolean" && len(variations) < 2 {
				return UsageError{fmt.Sprintf("--type %s needs at least two --variation key=value", strings.ToLower(typ))}
			}
			for _, v := range variations {
				k, value, found := strings.Cut(v, "=")
				if !found || k == "" {
					return UsageError{fmt.Sprintf("--variation %q must be key=value", v)}
				}
				req.InitialVariations = append(req.InitialVariations, api.InitialVariation{Key: k, Name: k, Value: value})
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
			var created json.RawMessage
			// One key for the whole invocation, so a retried create can't make two flags.
			err = s.API.Do(ctx, "POST", projectPath(org, project, "flags"),
				api.Request{Body: req, IdempotencyKey: api.NewIdempotencyKey()}, &created)
			if err != nil {
				return err
			}
			if g.json {
				return printJSON(env.Out, created)
			}
			fmt.Fprintf(env.Out, "Created %s (%s). It is off in every environment.\n", key, wireType)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "display name (default: the key)")
	f.StringVar(&typ, "type", "boolean", "boolean, string, number or json")
	f.StringArrayVar(&variations, "variation", nil, "a variation as key=value (repeatable; non-boolean types)")
	f.StringVar(&description, "description", "", "description")
	f.StringArrayVar(&tags, "tag", nil, "tag (repeatable)")
	f.BoolVar(&clientSide, "client-side", false, "make the flag available to client-side SDKs")
	return cmd
}

func newArchiveCmd(env Env, g *globals, archive bool) *cobra.Command {
	verb, past := "restore", "Restored"
	short := "Restore an archived flag"
	if archive {
		verb, past = "archive", "Archived"
		short = "Archive a flag"
	}
	var reason string
	var force bool
	cmd := &cobra.Command{
		Use:   verb + " <flag>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if err := checkKey("flag key", key); err != nil {
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
			if strings.TrimSpace(reason) == "" {
				if err := requireNoProjectReason(ctx, s, org, project); err != nil {
					return err
				}
			}
			req := api.Request{Reason: reason}
			if archive && force {
				req.Query = url.Values{"force": {"true"}}
			}
			if err := s.API.Do(ctx, "POST", projectPath(org, project, "flags", key, verb), req, nil); err != nil {
				return explainArchiveError(withReasonHint(err), key)
			}
			if g.json {
				return printJSON(env.Out, map[string]any{"flag": key, "archived": archive})
			}
			fmt.Fprintf(env.Out, "%s %s.\n", past, key)
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why you are making the change (required when any environment requires one)")
	if archive {
		cmd.Flags().BoolVar(&force, "force", false, "archive even though live traffic still evaluates the flag")
	}
	return cmd
}

// requireNoProjectReason refuses a project-level write without a reason when
// any environment in the project requires one, which is the rule the server
// applies to archive and restore.
func requireNoProjectReason(ctx context.Context, s *session, org, project string) error {
	envs, err := envsInOrder(ctx, s, org, project)
	if err != nil {
		return err
	}
	for _, e := range envs {
		if e.RequireChangeReason {
			return UsageError{`this project requires a change reason; pass --reason "<why>"`}
		}
	}
	return nil
}

const recentlyEvaluated = "FLAG_RECENTLY_EVALUATED: "

// explainArchiveError turns the server's "still evaluated" refusal (a field
// error on "flag" reading "FLAG_RECENTLY_EVALUATED: env1,env2") into a
// sentence that names the environments and the way past it.
func explainArchiveError(err error, key string) error {
	var apiErr *api.Error
	if !errors.As(err, &apiErr) {
		return err
	}
	for _, msg := range apiErr.Fields["flag"] {
		if envs, ok := strings.CutPrefix(msg, recentlyEvaluated); ok {
			apiErr.Message = fmt.Sprintf("%s is still evaluated by live traffic. Still evaluated in: %s. Pass --force to archive anyway.",
				key, strings.Join(strings.Split(envs, ","), ", "))
		}
	}
	return err
}
