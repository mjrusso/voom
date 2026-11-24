# Changelog

## Unreleased

## v1.7.0 - 2026-11-24

> [!IMPORTANT]
>
> **On MacOS, stop all VMs before upgrading if you use the default runtime
> directory (`/tmp/voom-$UID`).** Pause automation that starts or restarts VMs
> and wait for active `voom` commands to finish. Then stop the VMs with the
> version already installed.
>
> Linux users can upgrade with VMs running. On MacOS, the same applies if
> you already set `VOOM_RUNTIME_DIR` and keep it unchanged. If files under
> `/tmp/voom-$UID` cause errors after upgrading, see the
> [MacOS runtime recovery steps](docs/diagnostics-and-recovery.md#recover-vms-after-the-macos-runtime-directory-move).

- Move the default runtime directory on MacOS from `/tmp/voom-$UID` to
  `/Users/Shared/.voom-$UID`. System temp cleanup could delete the process
  record of a running VM and make Voom report it as stopped. A closed vfkit
  client socket left in the old directory no longer blocks VM startup.
- Store Linux guests' EFI variables in persistent VM storage on MacOS.
  [Copy any existing EFI store](docs/reference.md#migrate-an-efi-variable-store)
  after stopping the guest and before starting it with the new version.
- Report `unknown` in `voom info` and `voom list` on Linux and MacOS when
  Voom cannot verify the VM process state. Both commands previously showed
  `stopped` in that case. `voom info` JSON adds `status` and keeps the
  `running` boolean for compatibility.

## v1.6.0 - 2026-11-19

> [!IMPORTANT]
>
> **After upgrading, restart running VMs** to apply the new network policy
> and enable forwarding recovery. Point any `VOOM_GVPROXY` override at the
> gvproxy bundled with this release. If guests need direct tailnet access,
> configure the required IP and port exceptions before restarting.

- Block direct guest TCP and UDP connections to tailnet addresses by default.
  This prevents guests from making those connections using the host's Tailscale
  identity. Use `voom config allowed-destinations set` to allow
  specific IP and port pairs. Use `voom doctor` to check for missing blocks
  and stale exceptions. See [tailnet destinations](docs/reference.md#tailnet-destinations).
- Keep VMs running during port-forward recovery. If Voom cannot confirm a
  manual or automatic forwarding change, it retries in the background, keeps
  the affected ports reserved, and pauses further forwarding changes. Run
  `voom forward reconcile <name>` to retry immediately. Private egress recovery
  and failed-startup cleanup can still stop a VM. See
  [forwarding recovery](docs/diagnostics-and-recovery.md#forwarding-reports-uncertain-state).
- Show forwarding problems in VM diagnostics. `voom list`, `voom info`,
  and `voom doctor` report pending recovery and forwarding problems separately
  from whether the VM process is running. Voom records recovery errors in events
  and helper logs, including the reason and endpoint for private egress stops.
- Clean up automatically after the VM process exits on Linux. On Linux and
  MacOS, the VM watcher retries failed cleanup and records a stop event when
  cleanup completes.
- Hold one lock across shutdown and startup during a restart. Operations such
  as cloning or growing a disk require confirmation that the VM has stopped.
  Voom can still find a VM if a rename is interrupted.
- Keep logs from the previous VM run. Each start rotates helper and serial logs
  to `<name>.log.1`. Restarted watchers append to the current log.
- Include missing third-party license files in release archives and Nix packages,
  and repair links in the bundled license guide.

## v1.5.0 - 2026-10-22

- Add support for running MacOS guests, with features including a graphical
  console, SSH, cloning, reset, and manual TCP/UDP forwarding. A host running
  MacOS 27 or later, on Apple Silicon, is required. MacOS guests must also be
  running MacOS 27 or later. For more details, refer to the [MacOS guest
  guide](docs/macos-guests.md).
- Bundle vfkit and the Apple identity helper in MacOS release archives and Nix
  packages; a separate vfkit installation is no longer required on Apple hosts.
- Enforce read-only directory shares for Linux guests on MacOS hosts.
- Add `voom guest verify` to check SSH, guest health, mounts, and configured
  services on Linux and MacOS guests.
- Check vfkit compatibility in `voom doctor`. Add `voom debug helpers` to show
  which executables Voom uses.
- Include the user guides and command reference in release archives.

## v1.4.0 - 2026-10-14

- Add UDP port forwarding with `voom forward add --protocol udp`. TCP remains
  the default. TCP and UDP can use the same host address and port.
- Add shell completion for commands, VM names, and image names. Nix packages
  include completion files for Bash, Fish, and Zsh.
- Add command aliases: `shutdown` for `stop`, `reboot` for `restart`, `inspect`
  for `info`, `image info` for `image inspect`, and `delete` for `remove`.
- Avoid assigning SSH ports that are reserved by automatic forwards.
- Keep VMs running when an automatic forward cannot be saved before installation,
  or when gvproxy rejects a forward without installing it.
- When a port-forward request's outcome is unknown, Voom attempts to stop gvproxy
  to prevent the request from completing later. Once gvproxy is confirmed stopped,
  Voom stops the VM because its networking is gone and releases runtime forwarding
  reservations. Saved forwarding configuration is kept for the next start. If
  Voom cannot confirm that gvproxy has stopped, the affected host ports remain
  reserved.

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
