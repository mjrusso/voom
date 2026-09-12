# Changelog

## Unreleased

- Show disk capacity and saved egress configuration (`egress-config`) in
  `voom list`, and disk capacity and host allocation in `voom info`.
- Add per-VM explicit egress proxy configuration with live enable/disable,
  private TCP-to-Unix transport, and optional public CA guest metadata. Direct
  networking remains available. Requires the packaged gvproxy patch based on
  `9cfc86f66679ef0feed0f20ba1df558fe2bef5c6`; see `nix/GVPROXY.md`.
- Package gvproxy with private gateway TCP-to-Unix forwarding, host-only live
  control, guest API isolation, blocked guest access to host loopback, and
  confirmed tunnel shutdown. Pin gvisor-tap-vsock revision
  `9cfc86f66679ef0feed0f20ba1df558fe2bef5c6` with the maintained patch; include
  the build in release archives, the default Nix package, and development
  shells; include its third-party license material; and run its integration
  tests in Nix checks.

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
