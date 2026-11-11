# Changelog

## 0.1.0 — 2026-11-11

### Added

- First release of the `featureflip` command-line interface.
- `flags on` and `flags off` set a flag's state in one environment. Nothing is sent when it is already in that state. Environments that require a change reason need `--reason`, and environments that require confirmation ask you to type their key, or need `--yes` when no one is at a terminal.
- `flags status`, `flags list` (with `--env` for one environment's on/off state), `flags get`, `flags create`, `flags archive` and `flags restore`.
- `projects list`, `environments list` and `segments list`.
- `generate types` writes TypeScript types for a project's flag keys and values; `--check` fails when the file is out of date.
- `login`, `logout` and `whoami`. A token can also come from `FEATUREFLIP_TOKEN`.
- `--json` on every command, with errors as JSON on stdout, and documented exit codes.
