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
| `whoami` | Who the token acts as |
| `login` / `logout` | Save or remove a token on this machine |
| `version` | The CLI's version |

Every command takes `--json`, `--org`, `--project` and `--no-color`. Run `featureflip <command> --help` for the details, and `featureflip completion <shell>` for shell completion.

## Scripts and AI agents

With `--json`, stdout carries exactly one JSON document, whether the command succeeds or fails. A failure looks like this, with the API's error fields passed through:

```json
{
  "error": "flag_not_found",
  "message": "No flag 'new-chekout'.",
  "status": 404,
  "did_you_mean": ["new-checkout"]
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

## Configuration reference

| Variable | Purpose |
|---|---|
| `FEATUREFLIP_TOKEN` | API token. Wins over a saved one |
| `FEATUREFLIP_ORG` | Organization slug |
| `FEATUREFLIP_PROJECT` | Project key |
| `FEATUREFLIP_API_URL` | API base URL (default `https://api.featureflip.io`) |
| `NO_COLOR` | Disable colored output |

## Documentation

Full documentation: <https://featureflip.io/docs/integrations/cli/>

## License

Apache-2.0
