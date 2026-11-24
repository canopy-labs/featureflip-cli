package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/canopy-labs/featureflip-cli/internal/api"
	"github.com/canopy-labs/featureflip-cli/internal/exitcode"
	"github.com/canopy-labs/featureflip-cli/internal/importer"
	"github.com/canopy-labs/featureflip-cli/internal/importer/launchdarkly"
	"github.com/canopy-labs/featureflip-cli/internal/importer/statsig"
)

// sourceKeyError means the source vendor's API key is missing. Exit code 3.
type sourceKeyError struct{ msg string }

func (e sourceKeyError) Error() string { return e.msg }
func (sourceKeyError) ExitCode() int   { return exitcode.Auth }

func newImportCmd(env Env, g *globals) *cobra.Command {
	imp := groupCmd("import", "Import flags from another feature flag tool")
	imp.AddCommand(newImportStatsigCmd(env, g))
	imp.AddCommand(newImportLaunchDarklyCmd(env, g))
	return imp
}

// importFlags are the options every `import <vendor>` command shares.
type importFlags struct {
	envMap                                            []string
	apply, overwrite, carryState, client, breakFreeze bool
	reason                                            string
}

func (f *importFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringSliceVar(&f.envMap, "env-map", nil,
		"rename source environments: source=featureflip pairs, comma-separated or repeated")
	fl.BoolVar(&f.apply, "apply", false, "write the import to the Featureflip project (default: a dry run that writes nothing)")
	fl.BoolVar(&f.overwrite, "overwrite", false, "with --apply, replace the targeting of flags and segments that already exist and differ")
	fl.BoolVar(&f.carryState, "carry-state", false, "with --apply, turn each flag on where it is on in the source (default: every flag arrives off)")
	fl.BoolVar(&f.client, "client-side", false, "with --apply, make created flags available to client-side SDKs")
	fl.StringVar(&f.reason, "reason", "", "with --apply, the change reason recorded on every write (default: \"Imported from <vendor> with featureflip import\")")
	fl.BoolVar(&f.breakFreeze, "break-freeze", false, "with --apply, break an active change freeze for every write of this import (Owner/Admin; needs --reason)")
}

func (f *importFlags) check() error {
	if f.apply {
		if f.breakFreeze && strings.TrimSpace(f.reason) == "" {
			return UsageError{`--break-freeze needs --reason "<why>"; the reason is recorded in the audit log`}
		}
		return nil
	}
	for _, o := range []struct {
		name string
		set  bool
	}{{"--overwrite", f.overwrite}, {"--carry-state", f.carryState}, {"--client-side", f.client}, {"--reason", f.reason != ""}, {"--break-freeze", f.breakFreeze}} {
		if o.set {
			return UsageError{o.name + " only applies with --apply"}
		}
	}
	return nil
}

func newImportStatsigCmd(env Env, g *globals) *cobra.Command {
	var opts importFlags
	cmd := &cobra.Command{
		Use:   "statsig",
		Short: "Import a Statsig project's gates, dynamic configs and segments",
		Long: "Reads a Statsig project through the Statsig Console API and translates it: each feature gate\n" +
			"becomes a boolean flag, each dynamic config a JSON flag, and each segment a segment, with a\n" +
			"report of everything that translates differently or not at all.\n\n" +
			"Without --apply it is a dry run: nothing is written and no Featureflip account is needed.\n" +
			"With --apply it writes the translation to the Featureflip project. Flags arrive off unless\n" +
			"--carry-state is passed. Flags and segments that already exist are never changed unless they\n" +
			"differ and --overwrite is passed, so running the same import again changes nothing.\n\n" +
			"Set STATSIG_CONSOLE_API_KEY to a Console API key (a read-only key is enough).\n" +
			"Statsig environments keep their names as Featureflip environment keys unless --env-map renames them.",
		Example: "  STATSIG_CONSOLE_API_KEY=console-... featureflip import statsig\n" +
			"  featureflip import statsig --env-map development=dev,production=prod --json\n" +
			"  featureflip import statsig --env-map production=prod --project web --apply",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.check(); err != nil {
				return err
			}
			mapping, err := parseEnvMap(opts.envMap)
			if err != nil {
				return err
			}
			key := strings.TrimSpace(env.Getenv("STATSIG_CONSOLE_API_KEY"))
			if key == "" {
				return sourceKeyError{"no Statsig key: set STATSIG_CONSOLE_API_KEY to a Console API key (Statsig: Project Settings > Keys & Environments)"}
			}
			src := &statsig.Source{
				BaseURL: env.Getenv("STATSIG_CONSOLE_API_URL"),
				Key:     key,
				HTTP:    env.HTTPClient,
				Sleep:   env.Sleep,
			}
			return runImport(cmd.Context(), env, g, src, importer.Options{EnvMap: mapping}, opts)
		},
	}
	opts.register(cmd)
	return cmd
}

func newImportLaunchDarklyCmd(env Env, g *globals) *cobra.Command {
	var opts importFlags
	var ldProject string
	cmd := &cobra.Command{
		Use:   "launchdarkly",
		Short: "Import a LaunchDarkly project's flags, targeting and segments",
		Long: "Reads a LaunchDarkly project through LaunchDarkly's REST API and translates it: each flag with\n" +
			"its individual targets, rules, default rule, prerequisites and rollouts in every environment, and\n" +
			"each segment, with a report of everything that translates differently or not at all.\n\n" +
			"Without --apply it is a dry run: nothing is written and no Featureflip account is needed.\n" +
			"With --apply it writes the translation to the Featureflip project, prerequisites before the flags\n" +
			"that need them. Flags arrive off unless --carry-state is passed. Flags and segments that already\n" +
			"exist are never changed unless they differ and --overwrite is passed, so running the same import\n" +
			"again changes nothing.\n\n" +
			"Set LAUNCHDARKLY_ACCESS_TOKEN to an access token with the Reader role (LaunchDarkly: Organization\n" +
			"settings > Authorization). For LaunchDarkly's EU or federal instance, set LAUNCHDARKLY_API_URL to\n" +
			"https://app.eu.launchdarkly.com or https://app.launchdarkly.us.\n" +
			"LaunchDarkly environment keys become Featureflip environment keys unless --env-map renames them.",
		Example: "  LAUNCHDARKLY_ACCESS_TOKEN=api-... featureflip import launchdarkly --ld-project default\n" +
			"  featureflip import launchdarkly --ld-project default --env-map test=dev --json\n" +
			"  featureflip import launchdarkly --ld-project default --env-map test=dev --project web --apply",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.check(); err != nil {
				return err
			}
			if strings.TrimSpace(ldProject) == "" {
				return UsageError{"--ld-project is required: the key of the LaunchDarkly project to import (LaunchDarkly: Account settings > Projects)"}
			}
			mapping, err := parseEnvMap(opts.envMap)
			if err != nil {
				return err
			}
			token := strings.TrimSpace(env.Getenv("LAUNCHDARKLY_ACCESS_TOKEN"))
			if token == "" {
				return sourceKeyError{"no LaunchDarkly token: set LAUNCHDARKLY_ACCESS_TOKEN to an access token with the Reader role (LaunchDarkly: Organization settings > Authorization)"}
			}
			src := &launchdarkly.Source{
				BaseURL: env.Getenv("LAUNCHDARKLY_API_URL"),
				Project: strings.TrimSpace(ldProject),
				Token:   token,
				HTTP:    env.HTTPClient,
				Sleep:   env.Sleep,
			}
			return runImport(cmd.Context(), env, g, src, importer.Options{EnvMap: mapping}, opts)
		},
	}
	cmd.Flags().StringVar(&ldProject, "ld-project", "", "the key of the LaunchDarkly project to import (required)")
	opts.register(cmd)
	return cmd
}

// runImport reads and translates the source, then prints the dry run or, with
// --apply, plans and writes it.
func runImport(ctx context.Context, env Env, g *globals, src importer.Source, opts importer.Options, f importFlags) error {
	var s *session
	if f.apply {
		// Fail on a missing Featureflip token before reading the source.
		var err error
		if s, err = newSession(env, g); err != nil {
			return err
		}
	}
	res, err := src.Load(ctx, opts)
	if err != nil {
		return err
	}
	if !f.apply {
		if g.json {
			return printJSON(env.Out, res.Document())
		}
		return res.RenderText(env.Out)
	}

	org, project, err := s.Scope(ctx)
	if err != nil {
		return err
	}
	// An import sends several requests per flag, so a large one outruns the
	// API's per-token rate limit. Wait each limit out instead of stopping.
	client := api.New(api.Options{
		BaseURL: s.Settings.BaseURL, Token: s.Settings.Token, HTTPClient: env.HTTPClient, Sleep: env.Sleep,
		RateLimit: &api.RateLimitPolicy{MaxWait: 2 * time.Minute, Retries: 10, OnWait: func(d time.Duration) {
			fmt.Fprintf(env.Err, "Rate limited by the Featureflip API; waiting %s\n", d)
		}},
	})
	plan, err := importer.BuildPlan(ctx, client, res, importer.ApplyOptions{
		Org: org, Project: project, Overwrite: f.overwrite, CarryState: f.carryState,
		ClientSide: f.client, Reason: f.reason, BreakFreeze: f.breakFreeze,
	})
	if err != nil {
		return err
	}
	// Refuse an import over the plan's flag cap before asking anyone to
	// confirm it. Apply checks again, for callers that skip this one.
	if err := plan.CheckLimits(); err != nil {
		return err
	}
	// Like `flags on`, a live change in an environment that requires
	// confirmation needs the environment's key typed, or --yes.
	if !g.yes {
		for _, e := range plan.EnvironmentsToConfirm() {
			if err := confirm(env, e, fmt.Sprintf(`The import changes live flags in %s. Type "%s" to go ahead: `, e, e)); err != nil {
				return err
			}
		}
	}
	applyErr := plan.Apply(ctx, client, func(st *importer.Step) {
		fmt.Fprintf(env.Err, "%s %s %s\n", pastTense(st.Action), st.Kind, st.Key)
	})
	if g.json {
		if applyErr != nil {
			return applyErr // the error document is the one JSON value on stdout
		}
		return printJSON(env.Out, res.ApplyDocument(plan))
	}
	if err := res.RenderApplied(env.Out, plan); err != nil {
		return err
	}
	return applyErr
}

func pastTense(a importer.Action) string {
	if a == importer.ActUpdate {
		return "Updated"
	}
	return "Created"
}

// parseEnvMap reads --env-map's source=featureflip pairs.
func parseEnvMap(pairs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range pairs {
		from, to, ok := strings.Cut(p, "=")
		from, to = strings.TrimSpace(from), strings.TrimSpace(to)
		if !ok || from == "" || to == "" {
			return nil, UsageError{fmt.Sprintf("--env-map %q: use source=featureflip, e.g. production=prod", p)}
		}
		if importer.NormalizeKey(to) != to {
			return nil, UsageError{fmt.Sprintf("--env-map %q: %q is not a valid environment key (lowercase letters, digits, - and _)", p, to)}
		}
		if _, dup := out[from]; dup {
			return nil, UsageError{fmt.Sprintf("--env-map names %q twice", from)}
		}
		out[from] = to
	}
	return out, nil
}
