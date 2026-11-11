package cmd

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/canopy-labs/featureflip-cli/internal/api"
	"github.com/canopy-labs/featureflip-cli/internal/config"
)

func newWhoamiCmd(env Env, g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the identity and organization the token acts as",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := newSession(env, g)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			var raw json.RawMessage
			if err := s.API.Do(ctx, "GET", api.P("me"), api.Request{}, &raw); err != nil {
				return err
			}
			var me api.Me
			if err := json.Unmarshal(raw, &me); err != nil {
				return err
			}
			org, orgErr := s.Org(ctx)

			if g.json {
				var doc map[string]any
				if err := json.Unmarshal(raw, &doc); err != nil {
					return err
				}
				doc["organization"] = nil
				if orgErr == nil {
					doc["organization"] = org
				}
				return printJSON(env.Out, doc)
			}

			w := env.Out
			if me.Type == "service_token" {
				fmt.Fprintln(w, "Type: service token")
			} else {
				fmt.Fprintf(w, "Type: %s\n", me.Type)
			}
			fmt.Fprintf(w, "Name: %s\n", me.Name)
			if me.Email != nil {
				fmt.Fprintf(w, "Email: %s\n", *me.Email)
			}
			if me.Role != nil {
				fmt.Fprintf(w, "Role: %s\n", *me.Role)
			}
			if me.Type == "service_token" {
				if me.ProjectScope == nil {
					fmt.Fprintln(w, "Projects: all")
				} else {
					fmt.Fprintf(w, "Projects: %d project(s)\n", len(me.ProjectScope))
				}
			}
			switch {
			case orgErr == nil:
				fmt.Fprintf(w, "Organization: %s\n", org)
			case errors.As(orgErr, new(UsageError)):
				fmt.Fprintln(w, "Organization: (several; pass --org)")
			default:
				fmt.Fprintln(w, "Organization: (unavailable)")
			}
			return nil
		},
	}
}

func newLoginCmd(env Env, g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Save a token (and optional --org/--project defaults) for this machine",
		Long: "Reads a personal (ffp_...) or service (ffs_...) token from a hidden prompt, or from\n" +
			"stdin when piped, checks it against the API, and saves it to the config file\n" +
			"readable only by you. FEATUREFLIP_TOKEN, when set, still takes precedence.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			settings, file, root, err := loadSettings(env, true)
			if err != nil {
				return err
			}
			token, err := readToken(env)
			if err != nil {
				return err
			}

			var me api.Me
			client := newClient(env, settings.BaseURL, token)
			if err := client.Do(cmd.Context(), "GET", api.P("me"), api.Request{}, &me); err != nil {
				return err
			}

			file.Token = token
			if g.org != "" {
				file.Org = g.org
			}
			if g.project != "" {
				file.Project = g.project
			}
			if err := config.Save(root, file); err != nil {
				return err
			}
			if strings.TrimSpace(env.Getenv("FEATUREFLIP_TOKEN")) != "" {
				fmt.Fprintln(env.Err, "Warning: FEATUREFLIP_TOKEN is set and takes precedence over the saved token")
			}
			path := config.Path(root)
			if g.json {
				return printJSON(env.Out, map[string]string{"name": me.Name, "type": me.Type, "configPath": path})
			}
			fmt.Fprintf(env.Out, "Logged in as %s. Token saved to %s.\n", me.Name, path)
			return nil
		},
	}
}

// readToken asks for the token without echo at a terminal, or reads the first
// line of stdin when it's piped (e.g. from a secret manager).
func readToken(env Env) (string, error) {
	var token string
	if env.Terminal != nil && env.Terminal.Interactive() {
		t, err := env.Terminal.PromptSecret("Paste your Featureflip token: ")
		if err != nil {
			return "", err
		}
		token = t
	} else {
		line, err := bufio.NewReader(env.In).ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		token = line
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", UsageError{"no token given; paste it at the prompt or pipe it on stdin"}
	}
	return token, nil
}

func newLogoutCmd(env Env, g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove the saved token (keeps saved --org/--project defaults)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// No config directory means no saved token: nothing to remove.
			root, err := env.ConfigDir()
			var file config.File
			if err == nil {
				if file, _, err = config.Load(root); err != nil {
					return err
				}
			}
			if file.Token != "" {
				file.Token = ""
				if err := config.Save(root, file); err != nil {
					return err
				}
			}
			if g.json {
				return printJSON(env.Out, map[string]bool{"loggedOut": true})
			}
			fmt.Fprintln(env.Out, "Logged out.")
			return nil
		},
	}
}
