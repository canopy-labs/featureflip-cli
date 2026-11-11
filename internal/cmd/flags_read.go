package cmd

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/canopy-labs/featureflip-cli/internal/api"
)

func newFlagsListCmd(env Env, g *globals) *cobra.Command {
	var (
		envKey, search string
		tags           []string
		archived       bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the project's flags",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("env") {
				if err := checkKey("--env", envKey); err != nil {
					return err
				}
			}
			if len(tags) > 1 {
				return UsageError{"--tag can be given once; the API filters by a single tag"}
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
			if archived && envKey != "" {
				fmt.Fprintln(env.Err, "Warning: --env is ignored with --archived")
				envKey = ""
			}

			q := url.Values{}
			if search != "" {
				q.Set("search", search)
			}
			for _, t := range tags {
				q.Add("tag", t)
			}
			// The API returns active and archived flags together unless asked.
			q.Set("archived", fmt.Sprint(archived))
			raw, err := api.ListAll[json.RawMessage](ctx, s.API, projectPath(org, project, "flags"), q)
			if err != nil {
				return err
			}
			flags, err := api.Decode[api.FlagListItem](raw)
			if err != nil {
				return err
			}

			// One paginated compare of the environment against itself gives
			// every flag's state in it, instead of a request per flag.
			var state map[string]*api.SideSummary
			if envKey != "" {
				cq := url.Values{"base": {envKey}, "other": {envKey}}
				rows, err := api.ListAll[api.ComparisonRow](ctx, s.API, projectPath(org, project, "compare"), cq)
				if err != nil {
					return err
				}
				state = make(map[string]*api.SideSummary, len(rows))
				for _, r := range rows {
					state[r.FlagKey] = r.Base
				}
			}

			if g.json {
				out := make([]map[string]any, len(raw))
				for i, r := range raw {
					if err := json.Unmarshal(r, &out[i]); err != nil {
						return err
					}
					if state != nil {
						out[i]["enabled"] = nil
						if side := state[flags[i].Key]; side != nil {
							out[i]["enabled"] = side.IsEnabled
						}
					}
				}
				return printJSON(env.Out, out)
			}

			headers := []string{"key", "name", "type", "tags"}
			if state != nil {
				headers = append(headers, envKey)
			}
			rows := make([][]string, len(flags))
			for i, f := range flags {
				rows[i] = []string{f.Key, f.Name, f.Type, strings.Join(f.Tags, ",")}
				if state != nil {
					cell := "-"
					if side := state[f.Key]; side != nil {
						cell = onOff(env, g, side.IsEnabled)
					}
					rows[i] = append(rows[i], cell)
				}
			}
			return table(env.Out, headers, rows)
		},
	}
	cmd.Flags().StringVar(&envKey, "env", "", "also show each flag's on/off state in this environment")
	cmd.Flags().StringVar(&search, "search", "", "only flags whose key or name contains this text")
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "only flags with this tag")
	cmd.Flags().BoolVar(&archived, "archived", false, "list archived flags instead")
	return cmd
}

func newFlagsStatusCmd(env Env, g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "status <flag>",
		Short: "Show whether a flag is on or off in each environment",
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
			envs, configs, err := flagEnvironments(cmd, s, org, project, key)
			if err != nil {
				return err
			}

			type envState struct {
				Key     string `json:"key"`
				Name    string `json:"name"`
				Enabled *bool  `json:"enabled"`
			}
			states := make([]envState, len(envs))
			rows := make([][]string, len(envs))
			for i, e := range envs {
				states[i] = envState{Key: e.Key, Name: e.Name}
				rows[i] = []string{e.Key, "-"}
				if c, ok := configs[e.Key]; ok {
					enabled := c.IsEnabled
					states[i].Enabled = &enabled
					rows[i][1] = onOff(env, g, enabled)
				}
			}
			if g.json {
				return printJSON(env.Out, map[string]any{"flag": key, "environments": states})
			}
			return table(env.Out, []string{"environment", "state"}, rows)
		},
	}
}

func newFlagsGetCmd(env Env, g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "get <flag>",
		Short: "Show a flag's details, variations and per-environment state",
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
			var rawFlag json.RawMessage
			if err := s.API.Do(ctx, "GET", projectPath(org, project, "flags", key), api.Request{}, &rawFlag); err != nil {
				return err
			}
			var flag api.Flag
			if err := json.Unmarshal(rawFlag, &flag); err != nil {
				return err
			}
			var rawConfigs json.RawMessage
			if err := s.API.Do(ctx, "GET", projectPath(org, project, "flags", key, "environments"), api.Request{}, &rawConfigs); err != nil {
				return err
			}
			if g.json {
				return printJSON(env.Out, map[string]json.RawMessage{"flag": rawFlag, "environments": rawConfigs})
			}
			var configs []api.FlagEnvConfig
			if err := json.Unmarshal(rawConfigs, &configs); err != nil {
				return err
			}
			envs, err := envsInOrder(ctx, s, org, project)
			if err != nil {
				return err
			}

			w := env.Out
			fmt.Fprintf(w, "Key: %s\nName: %s\nType: %s\n", flag.Key, flag.Name, flag.Type)
			if flag.Description != nil && *flag.Description != "" {
				fmt.Fprintf(w, "Description: %s\n", *flag.Description)
			}
			if len(flag.Tags) > 0 {
				fmt.Fprintf(w, "Tags: %s\n", strings.Join(flag.Tags, ", "))
			}
			fmt.Fprintf(w, "Client-side: %s\n", yesNo(flag.ClientSideVisible))
			if flag.IsArchived {
				fmt.Fprintln(w, "Archived: yes")
			}

			variationKey := map[string]string{}
			vrows := make([][]string, len(flag.Variations))
			for i, v := range flag.Variations {
				variationKey[v.ID] = v.Key
				vrows[i] = []string{v.Key, v.Name, v.Value}
			}
			fmt.Fprintln(w, "\nVARIATIONS")
			if err := table(w, []string{"key", "name", "value"}, vrows); err != nil {
				return err
			}

			byEnv := map[string]api.FlagEnvConfig{}
			for _, c := range configs {
				byEnv[c.EnvironmentKey] = c
			}
			var erows [][]string
			for _, e := range envs {
				c, ok := byEnv[e.Key]
				if !ok {
					erows = append(erows, []string{e.Key, "-", "-", "-"})
					continue
				}
				def := variationKey[c.DefaultVariationID]
				if def == "" {
					def = "-"
				}
				// State last: color escapes count toward a column's width, so a
				// colored cell anywhere else would push the next columns out of line.
				erows = append(erows, []string{e.Key, def, c.Strategy, onOff(env, g, c.IsEnabled)})
			}
			fmt.Fprintln(w, "\nENVIRONMENTS")
			return table(w, []string{"environment", "default", "strategy", "state"}, erows)
		},
	}
}

// flagEnvironments returns the project's environments in display order and
// the flag's configuration in each, keyed by environment key.
func flagEnvironments(cmd *cobra.Command, s *session, org, project, key string) ([]api.Environment, map[string]api.FlagEnvConfig, error) {
	ctx := cmd.Context()
	var configs []api.FlagEnvConfig
	if err := s.API.Do(ctx, "GET", projectPath(org, project, "flags", key, "environments"), api.Request{}, &configs); err != nil {
		return nil, nil, err
	}
	envs, err := envsInOrder(ctx, s, org, project)
	if err != nil {
		return nil, nil, err
	}
	byEnv := make(map[string]api.FlagEnvConfig, len(configs))
	for _, c := range configs {
		byEnv[c.EnvironmentKey] = c
	}
	return envs, byEnv, nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
