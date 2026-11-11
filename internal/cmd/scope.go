package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/canopy-labs/featureflip-cli/internal/api"
	"github.com/canopy-labs/featureflip-cli/internal/config"
	"github.com/canopy-labs/featureflip-cli/internal/exitcode"
)

// authError is an authentication or visibility problem with the token. Exit 3.
type authError struct{ msg string }

func (e authError) Error() string { return e.msg }
func (authError) ExitCode() int   { return exitcode.Auth }

// loadSettings reads the config file and layers the environment over it,
// printing any warnings. It does not require a token (login needs that).
//
// With forWrite false (every command that only reads the file), a config file
// that can't be read is skipped with a warning and a missing config directory
// means nothing is saved, so the environment alone can still run the command.
// login passes true: it writes the file, so it must not replace one it
// couldn't read or carry on with nowhere to save.
func loadSettings(env Env, forWrite bool) (config.Settings, config.File, string, error) {
	var (
		file     config.File
		warnings []string
	)
	root, err := env.ConfigDir()
	if err != nil && forWrite {
		return config.Settings{}, config.File{}, "", err
	}
	if err == nil {
		file, warnings, err = config.Load(root)
		if err != nil {
			if forWrite {
				return config.Settings{}, config.File{}, "", err
			}
			file = config.File{}
			warnings = append(warnings, fmt.Sprintf("ignoring the config file (%v); fix or delete it", err))
		}
	}
	settings, more := config.Resolve(env.Getenv, file)
	if err := config.CheckBaseURL(settings.BaseURL); err != nil {
		return config.Settings{}, config.File{}, "", UsageError{err.Error()}
	}
	for _, w := range append(warnings, more...) {
		fmt.Fprintf(env.Err, "Warning: %s\n", w)
	}
	return settings, file, root, nil
}

// session is one authenticated run: the API client plus the resolved settings.
type session struct {
	API      *api.Client
	Settings config.Settings
	Env      Env
	g        *globals
}

func newSession(env Env, g *globals) (*session, error) {
	settings, _, _, err := loadSettings(env, false)
	if err != nil {
		return nil, err
	}
	if settings.Token == "" {
		return nil, api.ErrNoToken
	}
	return &session{
		API:      newClient(env, settings.BaseURL, settings.Token),
		Settings: settings,
		Env:      env,
		g:        g,
	}, nil
}

func newClient(env Env, baseURL, token string) *api.Client {
	return api.New(api.Options{BaseURL: baseURL, Token: token, HTTPClient: env.HTTPClient, Sleep: env.Sleep})
}

// Org is --org, else FEATUREFLIP_ORG or the saved default, else the token's
// only organization.
func (s *session) Org(ctx context.Context) (string, error) {
	if s.g.org != "" {
		return s.g.org, nil
	}
	if s.Settings.Org != "" {
		return s.Settings.Org, nil
	}
	orgs, err := api.ListAll[api.Org](ctx, s.API, api.P("orgs"), nil)
	if err != nil {
		return "", err
	}
	switch len(orgs) {
	case 0:
		return "", authError{"the token can see no organizations; check it in the Featureflip dashboard"}
	case 1:
		return orgs[0].Slug, nil
	}
	slugs := make([]string, len(orgs))
	for i, o := range orgs {
		slugs[i] = o.Slug
	}
	return "", UsageError{fmt.Sprintf(
		"the token can see %d organizations; pass --org or set FEATUREFLIP_ORG to one of: %s",
		len(orgs), strings.Join(slugs, ", "))}
}

// Project is --project, else FEATUREFLIP_PROJECT or the saved default, else
// the organization's only project. A project-scoped service token only sees
// its own projects, so its single project resolves here too.
func (s *session) Project(ctx context.Context, org string) (string, error) {
	if s.g.project != "" {
		return s.g.project, nil
	}
	if s.Settings.Project != "" {
		return s.Settings.Project, nil
	}
	projects, err := api.ListAll[api.Project](ctx, s.API, api.P("orgs", org, "projects"), nil)
	if err != nil {
		return "", err
	}
	switch len(projects) {
	case 0:
		return "", UsageError{fmt.Sprintf("organization %s has no projects this token can see", org)}
	case 1:
		return projects[0].Key, nil
	}
	keys := make([]string, len(projects))
	for i, p := range projects {
		keys[i] = p.Key
	}
	return "", UsageError{fmt.Sprintf(
		"organization %s has %d projects; pass --project or set FEATUREFLIP_PROJECT to one of: %s",
		org, len(projects), strings.Join(keys, ", "))}
}

// Scope resolves both, the common case for project-level commands.
func (s *session) Scope(ctx context.Context) (org, project string, err error) {
	if org, err = s.Org(ctx); err != nil {
		return "", "", err
	}
	if project, err = s.Project(ctx, org); err != nil {
		return "", "", err
	}
	return org, project, nil
}
