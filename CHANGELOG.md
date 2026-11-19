# Changelog

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
