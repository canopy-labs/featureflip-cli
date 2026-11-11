package cmd

import (
	"context"
	"sort"

	"github.com/spf13/cobra"

	"github.com/canopy-labs/featureflip-cli/internal/api"
)

func newFlagsCmd(env Env, g *globals) *cobra.Command {
	flags := groupCmd("flags", "List, inspect, flip and create feature flags")
	flags.AddCommand(
		newFlagsListCmd(env, g),
		newFlagsGetCmd(env, g),
		newFlagsStatusCmd(env, g),
		newFlipCmd(env, g, true),
		newFlipCmd(env, g, false),
		newFlagsCreateCmd(env, g),
		newArchiveCmd(env, g, true),
		newArchiveCmd(env, g, false),
	)
	return flags
}

func projectPath(org, project string, rest ...string) api.Path {
	return append(api.P("orgs", org, "projects", project), rest...)
}

// envsInOrder lists the project's environments in display order.
func envsInOrder(ctx context.Context, s *session, org, project string) ([]api.Environment, error) {
	envs, err := api.ListAll[api.Environment](ctx, s.API, projectPath(org, project, "environments"), nil)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(envs, func(i, j int) bool {
		if envs[i].SortOrder != envs[j].SortOrder {
			return envs[i].SortOrder < envs[j].SortOrder
		}
		return envs[i].Key < envs[j].Key
	})
	return envs, nil
}
