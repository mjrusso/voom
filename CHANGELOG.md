# Changelog

## Unreleased

- Avoid assigning SSH ports that are reserved by automatic forwards.
- Keep VMs running when an automatic forward cannot be saved before installation,
  or when gvproxy rejects a forward without installing it.

- Add command aliases: `shutdown` for `stop`, `reboot` for `restart`, `inspect`
  for `info`, `image info` for `image inspect`, and `delete` for `remove`.
- Add shell completion for commands, VM names, and image names.

- If Voom cannot confirm whether a port forward was added or removed, try to stop
  gvproxy so an outstanding request cannot later change forwarding or keep a host
  port bound. Once gvproxy is confirmed stopped, stop the VM because its networking
  is gone, and clear uncertain runtime forwarding state and automatic-forward
  reservations. Preserve saved forwarding configuration for the next start. If
  termination cannot be confirmed, keep the affected host ports reserved.

- Add UDP port forwarding with `voom forward add --protocol udp`. TCP remains
  the default. TCP and UDP can use the same host address and port.

## v1.3.0 - 2026-10-09

> [!IMPORTANT]
>
> **Upgrading from any earlier version:** Stop all running VMs before
> installing the new release. Voom now uses QMP instead of HMP for the QEMU
> monitor; the new binary cannot manage a VM that started prior to the upgrade.
>
> x86_64 QEMU VMs now use the q35 machine type instead of QEMU's default
> i440FX (`pc`) machine, including existing VMs on their next start. Guest PCI
> addresses and predictable network interface names change. Images that use
> Voom's cloud-init seed are unaffected, because its network configuration
> matches `en*` and `eth*` interfaces; custom images that configure a specific
> interface name may need updating.

- Add Linux/QEMU PCI Express passthrough (`voom pci`, alias `voom gpu`) for
  x86_64 VMs, by slot or single function, with host discovery, persisted
  assignments that record each function's vendor:device ID, and VFIO, IOMMU
  group, and memlock checks in `add`, `start`, `info`, and `doctor`.
- Include the last line of the QEMU or vfkit log in the error when the VM
  process exits during startup.
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
