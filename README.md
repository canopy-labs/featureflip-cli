# Featureflip CLI

`featureflip` manages [Featureflip](https://featureflip.io) feature flags from your terminal or a CI job: turn a flag on or off in one environment, check where it's on, list and create flags, and generate TypeScript types for your flag keys.

It's a single static binary with no runtime to install, and it talks to the public [Featureflip API](https://featureflip.io/docs/management-api/overview/).

## Install

**Homebrew** (macOS and Linux):

```sh
brew install canopy-labs/tap/featureflip
```

**Binary:** download the archive for your platform from the [latest release](https://github.com/canopy-labs/featureflip-cli/releases/latest), unpack it and put `featureflip` on your `PATH`. Builds exist for Linux, macOS and Windows on amd64 and arm64. `checksums.txt` lists each archive's SHA-256.

**Go** (1.26 or later):

```sh
go install github.com/canopy-labs/featureflip-cli/cmd/featureflip@latest
```

## Sign in

Create a token in the Featureflip dashboard: a personal token (`ffp_...`, Settings → API Tokens) or a service token (`ffs_...`, Organization Settings → Service Tokens).

At your own machine, save it once:

```sh
featureflip login
```

It asks for the token without echoing it, checks it, and saves it to a config file only you can read (`~/.config/featureflip/config.json` on Linux; the platform's user config directory elsewhere). `featureflip logout` removes it.

In CI, set the token as an environment variable instead. It takes precedence over a saved token:

```sh
export FEATUREFLIP_TOKEN=ffs_...
```

There is deliberately no `--token` flag, so a token never lands in your shell history or a process list.

`featureflip whoami` shows who the token acts as.

### Organization and project

Commands work on one organization and one project. Each is picked from, in order:

1. `--org` / `--project`
2. `FEATUREFLIP_ORG` / `FEATUREFLIP_PROJECT`
3. a default saved with `featureflip login --org <slug> --project <key>`
4. the only one the token can see

If there are several and you named none, the command lists them and exits with code 2.

## Turn a flag on or off

```sh
featureflip flags on new-checkout --env production
featureflip flags off new-checkout --env production
```

```
new-checkout: off → on in production
```

These set the state rather than toggle it. If the flag is already where you asked, nothing is sent and the command still succeeds:

```
new-checkout is already on in production
```

Some environments protect changes, and the CLI follows the same rules as the dashboard:

- **Change reason required:** pass `--reason "why"`. Without it the command stops before changing anything.
- **Confirmation required:** at a terminal you're asked to type the environment's key. In a script, pass `--yes` to confirm; without it the command refuses (exit code 6).

```sh
featureflip flags on new-checkout --env production --reason "Launch day" --yes
```

During a change freeze the API refuses the flip and the CLI exits with code 8, naming the freeze and when it ends:

```
Error: A change freeze covers production.
Change freeze "Holiday freeze" covers production until 2026-12-27 09:00 UTC. To break it for this one change, re-run with --break-freeze --reason "<why>".
```

An Owner or Admin can break the freeze for one change with `--break-freeze`, which needs `--reason`. With `--json`, the error document carries the freeze as `change_freeze` (`id`, `name`, `environment_key`, `ends_at`, `can_override`).

```sh
featureflip flags off new-checkout --env production --break-freeze --reason "Incident: checkout 500s"
```

Check a flag in every environment:

```sh
featureflip flags status new-checkout
```

```
ENVIRONMENT  STATE
development  on
staging      on
production   off
```

## Commands

| Command | What it does |
|---|---|
| `flags on <flag> --env <env>` | Turn a flag on in one environment (`--reason`, `--yes`) |
| `flags off <flag> --env <env>` | Turn a flag off in one environment (`--reason`, `--yes`) |
| `flags status <flag>` | On or off in each environment |
| `flags list` | The project's flags (`--env` adds on/off for one environment; `--search`, `--tag`, `--archived`) |
| `flags get <flag>` | Details, variations and per-environment state |
| `flags create <flag>` | Create a flag. Boolean by default; `--type string\|number\|json` with two or more `--variation key=value`. It starts off everywhere |
| `flags archive <flag>` | Archive a flag (`--reason`; `--force` if live traffic still evaluates it) |
| `flags restore <flag>` | Restore an archived flag (`--reason`) |
| `projects list` | Projects in the organization |
| `environments list` | Environments in the project, with their change guards |
| `segments list` | Segments in the project |
| `generate types` | TypeScript types for the project's flags (`--out`, `--check`) |
| `import statsig` | Translate a Statsig project and report what would import (`--env-map`). Writes nothing unless `--apply` (`--overwrite`, `--carry-state`, `--client-side`, `--reason`, `--break-freeze`) |
| `import launchdarkly` | Translate a LaunchDarkly project (`--ld-project`) and report what would import. Same options as `import statsig` |
| `whoami` | Who the token acts as |
| `login` / `logout` | Save or remove a token on this machine |
| `version` | The CLI's version |

Every command takes `--json`, `--org`, `--project` and `--no-color`. Run `featureflip <command> --help` for the details, and `featureflip completion <shell>` for shell completion.

## Scripts and AI agents

With `--json`, stdout carries exactly one JSON document, whether the command succeeds or fails. A failure looks like this, with the API's error fields passed through:

```json
{
  "did_you_mean": ["new-checkout"],
  "docs_url": "https://featureflip.io/docs/management-api/errors/not_found",
  "error": "not_found",
  "message": "Flag not found",
  "status": 404
}
```

Exit codes:

| Code | Meaning |
|---|---|
| 0 | Success, including a flip that changed nothing |
| 1 | Unexpected error: network, server error after retries |
| 2 | Usage: bad arguments, a missing `--reason`, an ambiguous organization or project |
| 3 | Authentication: no token, or the token was refused |
| 4 | Not found |
| 5 | The API rejected the change, e.g. a plan limit or a validation error |
| 6 | Confirmation was declined, or required without `--yes` |
| 7 | `generate types --check` found the file out of date |
| 8 | A change freeze covers the environment |

Requests that are safe to repeat are retried on transient gateway errors, and a create is sent with an idempotency key so a retry can't make two flags.

## Generate TypeScript types

```sh
featureflip generate types --out src/flags.ts
```

```ts
// Generated by `featureflip generate types` for project web. Do not edit.
export const flagKeys = [
  'new-checkout',
  'theme',
] as const;
export type FlagKey = (typeof flagKeys)[number];
export interface FlagValues {
  'new-checkout': boolean;
  'theme': 'dark' | 'light';
}
```

Boolean and number flags map to `boolean` and `number`, a string flag to the union of its variation values, and a JSON flag to `unknown`. The output is sorted and has no timestamp, so it diffs cleanly. To fail CI when someone changes flags without regenerating:

```sh
featureflip generate types --out src/flags.ts --check
```

## Import from Statsig

```sh
STATSIG_CONSOLE_API_KEY=console-... featureflip import statsig
```

This reads a Statsig project through the Statsig Console API and prints what would be imported. Each feature gate becomes a boolean flag, each dynamic config becomes a JSON flag, and each segment comes across too. The run is read-only on both sides, so it needs no Featureflip token. A read-only Console API key is enough.

The report sorts every item three ways:

- **Blocked** items would behave differently in Featureflip, so they are not imported. Examples are a rule that checks another gate, or a partial rollout of a config with three or more values.
- **Warnings** import, but need something from you. For example, a key renamed to fit Featureflip's key format means the code that reads it must change. Another is an attribute Statsig derives for you, like country from the IP address.
- **Notes** record decisions you should know about.

The report ends with every context attribute the imported rules read.

Two things never carry over:

- **Rollout membership.** Rollouts keep their percentage, but Featureflip buckets users with its own hash, so a 30% rollout reaches a different 30%.
- **Experiments, layers, holdouts and autotunes.** They are not read.

Statsig environments keep their names as Featureflip environment keys. Rename them with `--env-map development=dev,production=prod`. `--json` prints the full translation: every flag's per-environment rules, the segments and the report.

### Writing the import

```sh
featureflip login
STATSIG_CONSOLE_API_KEY=console-... featureflip import statsig --env-map production=prod --project web --apply
```

`--apply` writes the translation to a Featureflip project. Every source environment must map to an environment the project already has. It reads the project first, then decides what to do with each flag and segment:

- **Created** when nothing in the project has its key. Segments are written first, then each flag with its rules and its default in every environment.
- **Unchanged** when it already matches the source. Running the same import again writes nothing.
- **Left untouched** when it exists and differs. `--overwrite` replaces its rules and default in the environments that differ, and replaces its prerequisites there with the source's. Statsig has none, so they are removed. A flag whose type or variations differ, or one that is archived, is never changed.
- **Blocked** items in the report are never written, nor are flags that use a segment that isn't.

Imported flags arrive off. `--carry-state` turns each one on in the environments where it is on in Statsig. To turn on flags an earlier import left off, re-run with `--carry-state --overwrite`; where only the on/off state differs, that is all it changes. Flags are server-side only unless you pass `--client-side`, and Statsig serves every gate to its client SDKs, so mark the ones your browser and mobile apps read.

Before it asks you to confirm anything or writes anything, the import stops when the flags to create would take the project past the plan's flag cap (exit code 5). Like `flags on`, it asks you to type the key of each environment that requires confirmation where it would change live flags (turning one on, or changing an existing one), or takes `--yes`. Creating flags that stay off needs no confirmation.

It stops at the first write the API refuses and says which item it was writing. When the flag itself was created before the failure, the summary says it was created but not fully configured. With `--json`, the error document carries the same `target` a finished import prints, where that flag reads `"created": true, "done": false`. If a change freeze covers a write, the import stops with exit code 8 and names the freeze. An Owner or Admin can break it for every write of the import with `--break-freeze --reason "<why>"`. Re-running is safe: finished items read as unchanged, and a flag left half written reads as differing, which `--overwrite` finishes. It waits out the API's rate limit, printing each wait; Ctrl-C ends a wait at once. Every write except the flag create itself records the change reason "Imported from Statsig with featureflip import", or the one you pass with `--reason`.

## Import from LaunchDarkly

```sh
LAUNCHDARKLY_ACCESS_TOKEN=api-... featureflip import launchdarkly --ld-project default
```

This reads a LaunchDarkly project through LaunchDarkly's REST API and prints what would be imported. It works the same way as `import statsig`: the same report, the same `--env-map`, `--json` and `--apply` options, and the same rules for what is created, left alone or blocked. An access token with the Reader role is enough. For the EU or federal instance, set `LAUNCHDARKLY_API_URL` to `https://app.eu.launchdarkly.com` or `https://app.launchdarkly.us`.

Each flag comes across with its individual targets, rules, default rule, prerequisites and rollouts in every environment. `--apply` writes a flag's prerequisites before the flag. Some things translate differently:

- **Off variation.** A Featureflip flag serves its default variation while it is off, and a boolean flag serves `false`. So the default holds LaunchDarkly's off variation, and a last rule named "Default rule" serves LaunchDarkly's default rule when the two differ. A boolean flag whose off variation is `true` is blocked when it is off in any environment.
- **Context kinds.** Featureflip contexts are flat. An attribute of a non-user context kind becomes a prefixed attribute, such as `organization.plan` or `organization.key`, and your app must send it that way. A user's key is `user_id`.
- **Segments.** LaunchDarkly segments belong to one environment. A segment that is one set of conditions, the same in every environment, becomes a Featureflip segment. Any other segment is copied into the rules of the flags that use it. Big (unbounded) segments are blocked, because the API doesn't list their members.
- **Rollouts.** Percentages are whole numbers in Featureflip, and a rollout serves one variation a percentage and splits the rest evenly. A split across three or more variations that can't be written that way is blocked. Rollout membership doesn't carry over, as with Statsig.
- **String comparisons** ignore case in Featureflip, and are case-sensitive in LaunchDarkly.

Experiment results, metrics, scheduled changes, approvals, flag triggers, workflows and release pipelines are not read. Archived flags are skipped. A flag that LaunchDarkly makes available to client-side SDKs is created client-side visible.

## Configuration reference

| Variable | Purpose |
|---|---|
| `FEATUREFLIP_TOKEN` | API token. Wins over a saved one |
| `FEATUREFLIP_ORG` | Organization slug |
| `FEATUREFLIP_PROJECT` | Project key |
| `FEATUREFLIP_API_URL` | API base URL (default `https://api.featureflip.io`) |
| `STATSIG_CONSOLE_API_KEY` | Statsig Console API key, for `import statsig` |
| `LAUNCHDARKLY_ACCESS_TOKEN` | LaunchDarkly access token (Reader role), for `import launchdarkly` |
| `LAUNCHDARKLY_API_URL` | LaunchDarkly base URL (default `https://app.launchdarkly.com`) |
| `NO_COLOR` | Disable colored output |

## Documentation

Full documentation: <https://featureflip.io/docs/integrations/cli/>

## License

Apache-2.0
