# Changelog

## 0.3.0 — 2026-11-24

### Added

- `import statsig` reads a Statsig project through the Statsig Console API (`STATSIG_CONSOLE_API_KEY`). It prints how each feature gate, dynamic config and segment would translate into flags and segments, with a report of everything that translates differently or not at all and the context attributes the rules read. It writes nothing and needs no Featureflip token.
- `import statsig --apply` writes the translation to a Featureflip project. It creates what is missing, leaves what already matches, and leaves what differs alone unless you pass `--overwrite`, so running it again changes nothing. Flags arrive off unless you pass `--carry-state`. It checks the plan's flag cap before it asks you to confirm anything or writes. A flag created before a failed write is reported as created but not fully configured, and with `--json` the error document carries what was written as `target`.
- `import launchdarkly --ld-project <key>` reads a LaunchDarkly project through its REST API (`LAUNCHDARKLY_ACCESS_TOKEN`, a Reader-role token; `LAUNCHDARKLY_API_URL` for the EU or federal instance). It translates each flag's individual targets, rules, default rule, prerequisites and rollouts, and each segment, with the same report and the same `--apply` options as `import statsig`.
- `import --apply` takes `--break-freeze` (with `--reason`), like `flags on` and `flags off`, to write through an active change freeze. Without it, a frozen write stops the import with exit code 8 and names the freeze.
- An import now writes prerequisites, each flag after the flags it depends on, and compares them on a re-run.

### Fixed

- Ctrl-C now ends a wait between retries at once, including a rate-limit wait of up to two minutes. Before, every command slept the wait out first.

## 0.2.0 — 2026-11-18

### Added

- `flags on` and `flags off` explain a change freeze: they exit with code 8 (it was 1) and print the freeze's name, environment and end time. With `--json`, the error carries the freeze as `change_freeze`.
- `--break-freeze` on `flags on` and `flags off` lets an Owner or Admin break an active change freeze for one change. It needs `--reason`.

## 0.1.0 — 2026-11-11

### Added

- First release of the `featureflip` command-line interface.
- `flags on` and `flags off` set a flag's state in one environment. Nothing is sent when it is already in that state. Environments that require a change reason need `--reason`, and environments that require confirmation ask you to type their key, or need `--yes` when no one is at a terminal.
- `flags status`, `flags list` (with `--env` for one environment's on/off state), `flags get`, `flags create`, `flags archive` and `flags restore`.
- `projects list`, `environments list` and `segments list`.
- `generate types` writes TypeScript types for a project's flag keys and values; `--check` fails when the file is out of date.
- `login`, `logout` and `whoami`. A token can also come from `FEATUREFLIP_TOKEN`.
- `--json` on every command, with errors as JSON on stdout, and documented exit codes.
