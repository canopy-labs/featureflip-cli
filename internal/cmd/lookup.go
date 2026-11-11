package cmd

import (
	"context"
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/canopy-labs/featureflip-cli/internal/api"
)

// listCmd builds `<noun> list`: fetch every page of path(ctx), then print the
// raw items with --json or a table built by rows.
func listCmd[T any](env Env, g *globals, noun, short string,
	path func(ctx context.Context, s *session) (api.Path, error),
	headers []string, rows func(items []T) [][]string,
) *cobra.Command {
	list := &cobra.Command{
		Use:   "list",
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := newSession(env, g)
			if err != nil {
				return err
			}
			p, err := path(cmd.Context(), s)
			if err != nil {
				return err
			}
			raw, err := api.ListAll[json.RawMessage](cmd.Context(), s.API, p, nil)
			if err != nil {
				return err
			}
			if g.json {
				if raw == nil {
					raw = []json.RawMessage{}
				}
				return printJSON(env.Out, raw)
			}
			items, err := api.Decode[T](raw)
			if err != nil {
				return err
			}
			return table(env.Out, headers, rows(items))
		},
	}
	parent := groupCmd(noun, short)
	parent.AddCommand(list)
	return parent
}

func newProjectsCmd(env Env, g *globals) *cobra.Command {
	return listCmd(env, g, "projects", "List the organization's projects",
		func(ctx context.Context, s *session) (api.Path, error) {
			org, err := s.Org(ctx)
			return api.P("orgs", org, "projects"), err
		},
		[]string{"key", "name"},
		func(items []api.Project) [][]string {
			rows := make([][]string, len(items))
			for i, p := range items {
				rows[i] = []string{p.Key, p.Name}
			}
			return rows
		})
}

func newEnvironmentsCmd(env Env, g *globals) *cobra.Command {
	return listCmd(env, g, "environments", "List the project's environments",
		func(ctx context.Context, s *session) (api.Path, error) {
			org, project, err := s.Scope(ctx)
			return api.P("orgs", org, "projects", project, "environments"), err
		},
		[]string{"key", "name", "confirm", "reason"},
		func(items []api.Environment) [][]string {
			rows := make([][]string, len(items))
			for i, e := range items {
				rows[i] = []string{e.Key, e.Name, yes(e.RequireConfirmation), yes(e.RequireChangeReason)}
			}
			return rows
		})
}

func newSegmentsCmd(env Env, g *globals) *cobra.Command {
	return listCmd(env, g, "segments", "List the project's segments",
		func(ctx context.Context, s *session) (api.Path, error) {
			org, project, err := s.Scope(ctx)
			return api.P("orgs", org, "projects", project, "segments"), err
		},
		[]string{"key", "name", "description"},
		func(items []api.Segment) [][]string {
			rows := make([][]string, len(items))
			for i, sg := range items {
				desc := ""
				if sg.Description != nil {
					desc = *sg.Description
				}
				rows[i] = []string{sg.Key, sg.Name, desc}
			}
			return rows
		})
}

func yes(b bool) string {
	if b {
		return "yes"
	}
	return ""
}
