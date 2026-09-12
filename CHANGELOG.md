# Changelog

## Unreleased

- Record each helper's PID and system-specific start identity at launch. Before
  signaling a helper, lifecycle cleanup requires those values to match the
  running process.
  Start cleans surviving runtime; stop, remove, and disk reset retain recovery
  records when identity or termination cannot be verified.
- Keep unrelated VM operations available during startup, disk reset, and waits
  for a busy VM. Preserve concurrent configuration changes during NixOS switch.
- Fix Nix builds to report the correct version, commit, and build date.

## v1.1.0 - 2026-08-28

- Add `voom skill` to print version-matched agent instructions for using Voom.
- Add `voom events` for replaying and following best-effort VM lifecycle and
  automatic-forward change events.
- Add `--bind` and `--lan` options to `voom forward auto enable` so automatic
  forwards can be exposed on a chosen host interface instead of loopback only.

## v1.0.0 - 2026-06-01

- Initial release.
