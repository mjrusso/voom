# Changelog

## Unreleased

> [!IMPORTANT]
>
> **Upgrading from any earlier version:** Stop all running VMs before
> installing the new release. Voom now uses QMP instead of HMP for the QEMU
> monitor; the new binary cannot manage a VM that started prior to the upgrade.

- Add Linux/QEMU USB passthrough by stable controller route and port, with host
  discovery, live attach and detach, persisted VM assignments, permission
  checks, and QEMU capability and runtime status diagnostics.
- Standardize CLI names: use `list` and `remove` as the canonical commands,
  retain `ls` and `rm` as aliases, and retain `destroy` as an alias for
  `voom remove`.
- Restrict runtime roots and per-VM runtime directories to the current user,
  and reject runtime directories that are symlinks or owned by another user.

## v1.2.1 - 2026-09-16

- Fix helper shutdown on MacOS when an exited process is no longer returned by
  the system process query.

## v1.2.0 - 2026-09-16

> [!IMPORTANT]
>
> **Upgrading from any earlier version:** Stop all running VMs before
> installing the new release. Voom now uses process identity records instead of
> runtime PID files; the new binary cannot stop or restart a VM that started
> prior to the upgrade.

- Add optional access to an external HTTP CONNECT proxy for each VM. Voom makes
  the proxy available to guest applications at `192.168.127.1:3128` and connects
  it to a Unix socket assigned to the VM. Applications must opt in to the proxy,
  so traffic that does not use it continues over the VM's normal network
  connection. Voom can also publish an optional CA bundle for guest software to
  install.
- Add `voom config egress` commands to manage proxy access. External managers
  can use `set --disabled` to save an attachment without enabling it.
  `--expect-id` rejects a change unless the immutable VM ID matches. Enable and
  disable work while the VM runs. `voom remove` does not remove or stop the
  external proxy.
- Package gvproxy with private gateway TCP-to-Unix forwarding and a live
  control API available only to the host. The package blocks guest access to
  gvproxy's host APIs and host loopback services, and confirms tunnel shutdown.
  Pin gvisor-tap-vsock revision
  `9cfc86f66679ef0feed0f20ba1df558fe2bef5c6` with the maintained patch. Include
  gvproxy in release archives, the default Nix package, and development shells.
  Include its third-party license material and run its integration tests in
  Nix checks.
- Record each helper's PID and system-specific start identity at launch. Voom
  signals a helper only when both values match the running process. Before
  boot, `voom start` tries to remove stale runtime state. If Voom cannot verify
  a helper's identity or confirm termination, it keeps the recovery records.
  This rule applies to `voom start`, `voom stop`, `voom remove`, and
  `voom disk reset`.
- Keep installed automatic forwards when the guest report is missing, stale,
  or malformed. Apply bind and offset changes immediately. Voom serializes
  reconciliation per VM and writes runtime forwarding state atomically. If
  Voom cannot save that state, it attempts to roll back the matching gvproxy
  changes. `voom start` and `voom forward auto enable` leave reconciliation to
  the watcher instead of waiting for it. `voom forward auto reconcile` returns
  an error for a stopped VM or an unavailable or invalid guest report.
- Allow operations for other VMs while one VM starts, resets its disk, or waits
  for a lock. Preserve concurrent configuration changes during
  `voom nixos switch`.
- Show virtual disk capacity and saved egress configuration (`egress-config`)
  in `voom list`. Show virtual disk capacity and allocated host disk space in
  `voom info`.
- Fix Nix builds to report the correct version, commit, and build date.

## v1.1.0 - 2026-08-28

- Add `voom skill` to print version-matched agent instructions for using Voom.
- Add `voom events` for replaying and following best-effort VM lifecycle and
  automatic-forward change events.
- Add `--bind` and `--lan` options to `voom forward auto enable` so automatic
  forwards can be exposed on a chosen host interface instead of loopback only.

## v1.0.0 - 2026-06-01

- Initial release.
