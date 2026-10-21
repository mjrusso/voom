# Reference

[README](../README.md) · [Command reference](commands/voom.md)

Voom manages local development VMs through the host's VM stack. Voom does not
build images or run a persistent background control plane. Release and Nix
installations bundle gvproxy and, on MacOS, patched vfkit; Apple Silicon
packages also include the native identity helper. QEMU remains an external
dependency on Linux. Per-VM watchers manage automatic forwarding and MacOS
runtime cleanup while those VMs run.

Images must be explicitly imported, VMs must be created explicitly
(referencing an existing, already-imported image), and optional guest
integrations are always opt-in.

## Commands overview

| Area      | Commands                                                                                                |
|-----------|---------------------------------------------------------------------------------------------------------|
| Lifecycle | `create`, `start`, `stop`, `restart`, `remove`, `clone`, `rename`                                       |
| Images    | `image import`, `image inspect`, `image list`, `image remove`                                           |
| Compute   | `resources cpus`, `resources memory`                                                                    |
| Disk      | `resources disk grow`, `disk reset`                                                                     |
| Access    | `ssh`, `console`, `gui`, `ssh-config`, `config show`, `config ssh-port`                                 |
| Forwards  | `forward add`, `forward remove`, `forward list`, `forward discover`, `forward auto enable`/`disable`/`offset` |
| Egress    | `config egress set`, `config egress clear`, `config egress enable`, `config egress disable`             |
| Shares    | `share add`, `share remove`, `share list`                                                               |
| USB       | `usb discover`, `usb add`, `usb remove`, `usb list`                                                     |
| PCI       | `pci discover`, `pci add`, `pci remove`, `pci list` (alias: `gpu`)                                      |
| NixOS     | `nixos switch`                                                                                          |
| Inspect   | `list`, `info`, `logs`, `events`, `doctor`, `guest verify`, `guest ports`, `debug paths`, `debug helpers`, `version`, `skill`            |

The [support tables](../README.md#supported-hosts-and-guests) describe which commands
apply to each host and guest. Most non-streaming commands accept `--output
json`; `ssh`, `console`, and `nixos switch` use text or subprocess output.

See the [command reference](commands/voom.md) for flags and arguments,
and [aliases and shell
completion](#aliases-and-shell-completion) for shortcuts and
shell setup.

## Directories

| Purpose                     | Linux default                                  | Override           |
|-----------------------------|------------------------------------------------|--------------------|
| Configuration               | `$XDG_CONFIG_HOME/voom` or `~/.config/voom`    | `VOOM_CONFIG_DIR`  |
| State (source of truth)     | `$XDG_DATA_HOME/voom` or `~/.local/share/voom` | `VOOM_STATE_DIR`   |
| Cache (logs)                | `$XDG_CACHE_HOME/voom` or `~/.cache/voom`      | `VOOM_CACHE_DIR`   |
| Runtime (sockets, process records) | `$XDG_RUNTIME_DIR/voom` or `/tmp/voom-$UID` | `VOOM_RUNTIME_DIR` |

Run `voom debug paths` to inspect resolved directories. On MacOS, state and
configuration default to `~/Library/Application Support/voom`, cache to
`~/Library/Caches/voom`, and runtime to `/tmp/voom-$UID`. MacOS uses the same
`VOOM_*` overrides and ignores `XDG_*` variables.

For gvproxy, vfkit, and the Apple identity helper, Voom checks an existing
`VOOM_GVPROXY`, `VOOM_VFKIT`, or `VOOM_MACOS_IDENTITY` override first, then a
binary beside its own executable, then `PATH`. Packaged installations provide
the sibling helpers; overrides remain available for development. Run `voom
debug helpers --output json` to see the selected executable paths. Startup
checks the extensions required by the VM, and `voom doctor` probes
packaged-helper compatibility. Stock vfkit cannot replace the patched build
for MacOS guests or read-only directory shares. Other runtime overrides
include `VOOM_VIRTIOFSD` and `VOOM_QEMU_AARCH64_UEFI`.

Set the four directory overrides in the table to disposable directories to
keep tests separate from your normal VMs. See
[CONTRIBUTING.md](../CONTRIBUTING.md) for examples.

## Boot firmware and disk formats

On `aarch64` QEMU hosts, Voom looks for UEFI firmware in this order:

```text
/usr/share/AAVMF/AAVMF_CODE.fd
/usr/share/qemu/edk2-aarch64-code.fd
/usr/share/edk2/aarch64/QEMU_EFI.fd
```

Set `VOOM_QEMU_AARCH64_UEFI=/path/to/firmware.fd` when your distribution
stores firmware elsewhere. For Linux guests on MacOS, vfkit uses direct kernel
boot metadata from the imported image or an EFI-bootable disk. MacOS guests
use the disk and Apple platform files from their bundle. New vfkit VMs use
`disk.raw`; existing Linux VMs using `disk.img` keep that filename until
reset. Both contain raw disks, and image import accepts either extension.

## Networking and SSH

Each VM runs its own gvproxy process for ordinary outbound networking and
host-to-guest port mappings. Voom does not create a host bridge or assign a
separate routable address to each VM. SSH maps a persisted host port to guest
`192.168.127.3:22`; it remains available for MacOS guests even though
application forwarding is unsupported there.

Voom's gvproxy build blocks guest access to host loopback services through
`192.168.127.254`, `host.containers.internal`, and `host.docker.internal`.
[Explicit proxy attachments](proxy-attachments.md) provide a separate, per-VM
route to a configured host Unix socket for Linux guests.

`voom ssh` and generated `ssh-config` entries disable SSH host-key
verification and do not save host keys in `known_hosts`. These connections
target local, disposable VMs whose keys may change after reset. `guest verify`
collects host-key fingerprints but does not authenticate them against a
trusted baseline.

For MacOS guests, `voom ssh` ignores the host's SSH configuration and, when an
identity is recorded, uses that identity exclusively. Linux guests use normal
OpenSSH configuration; a recorded identity adds an `IdentityFile` without
forcing `IdentitiesOnly`. See [SSH and file
transfer](../README.md#ssh-and-file-transfer) for configuration and `scp`
examples.

## Images

**Names and IDs.** VM and image names match `^[a-zA-Z0-9][a-zA-Z0-9_-]*$`.
Voom assigns each object a separate, stable ID. Renaming a VM changes its name
without changing its ID.

**Disks.** Each VM disk is an independent copy of the image disk. `voom image
remove` blocks while any VM references the image, unless `--force` is used; a
forced removal leaves existing VM disks and records in place. A running VM
continues to run, and `start` returns without loading the image. Booting a
stopped VM and guest verification still require the image record; reset to an
available image before restarting. Cloning a stopped VM uses its own storage
and metadata, so it does not require the original image record.

## Storage format

MacOS image and VM records require schema 2. MacOS VMs require a storage
generation selecting their boot-file directory. New Linux VMs also use schema
2 and storage generations. Existing Linux VMs remain readable with schema 1
and move to schema 2 on reset. Linux image records remain schema 1. These
versions describe Voom's stored records; the source bundle's `image.json`
manifest still uses schema 1.

Older released binaries cannot read schema 2 records and may fail to list VMs
or images when those records are present.

## Storage replacement

On both Linux and MacOS, reset copies replacement storage into a new
directory, then saves the VM record to select that copy and its image/access
metadata. A failure before that save leaves the old storage selected, though
the VM may already be stopped. If the save's outcome is uncertain, Voom keeps
both copies. The next successful start or reset uses the saved record to
remove unused storage. Cleanup errors explicitly say when reset already
committed.

## Aliases and shell completion

`shutdown` is an alias for `stop`, and `reboot` is an alias for `restart`.
`restart` and `reboot` stop and start the VM and its host helpers, including
gvproxy. Use `inspect` or `info` to inspect a VM, and `image inspect` or
`image info` to inspect an image. Every `list` command accepts `ls`. Every
`remove` command accepts `rm` and `delete`; `voom remove` also accepts
`destroy`. `gpu` is an alias for `pci`.

Generate completion scripts for Bash, Zsh, Fish, or PowerShell with `voom
completion <shell>`. Completion includes command names, existing VM and image
names, and image names for `--image`.

The Nix package installs Bash, Fish, and Zsh completion files in their
standard directories. Fish loads them automatically from Nix profiles. Bash
and Zsh use them when their completion systems are enabled.

Bash requires the `bash-completion` package.

To enable completion in the current session:

```bash
source <(voom completion bash)
```

```zsh
# Initialize Zsh completion if it is not already enabled.
autoload -Uz compinit && compinit
source <(voom completion zsh)
```

```fish
voom completion fish | source
```

```powershell
voom completion powershell | Out-String | Invoke-Expression
```

For installation instructions, run `voom completion <shell> --help`.

## State and runtime layout

State is the persistent source of truth. JSON files are written atomically via
temp-file-and-rename. Temporary files do not replace the saved record until
rename succeeds. Malformed saved records cause load errors; use `voom doctor`
to investigate state inconsistencies.

```text
<state>/state.json                          # name → ID index
<state>/images/<image-id>/image.json        # metadata, capabilities
<state>/images/<image-id>/disk.qcow2|raw    # imported image disk
<state>/vms/<vm-id>/vm.json                 # config, forwards, shares, USB and PCI assignments
<state>/vms/<vm-id>/disk.qcow2|raw          # legacy Linux VM disk copy
<state>/vms/<vm-id>/storage-<generation>/   # VM disk and any Apple platform files
<state>/locks/...                           # state and per-VM locks
```

MacOS image and VM storage also includes `HardwareModel`, `MachineIdentifier`,
and `AuxiliaryStorage`. Imported images preserve `bundle.json`,
`restore.json`, and `toolchain.json`. New VMs' active disks and any platform
files live under `<state>/vms/<vm-id>/storage-<generation>/`, selected by
`vm.json`. These files survive stop and restart. Legacy Linux vfkit VMs may
retain `disk.img`.

Runtime files are disposable. Notable paths under `<runtime>/vms/<vm-id>/`:

- `vm.process.json`, `gvproxy.process.json`, `auto-forward.process.json`,
  `runtime-watch.process.json`, `virtiofs-<tag>.process.json` — process IDs
  and system-specific start identities
- `seed.img` — Linux-only regenerated cloud-init NoCloud disk
  (`meta-data`, `user-data`, `network-config`)
- `control/metadata.json`, `control/mounts.json`, `control/ports.json` —
  Linux-only host side of the reserved `voom-control` share
- `network.sock`, `qemu.mon`, `vfkit.sock`, `virtiofs-<tag>.sock` — driver and
  helper sockets

Cache holds disposable logs: `serial.log`, `qemu.log` or `vfkit.log`,
`gvproxy.log`, `auto-forward.log`, `share-mount.log`, `runtime-watch.log`, and
the global `events.jsonl` event stream. The stream retains one rotated
generation at `events.jsonl.1` and coordinates writers through `events.lock`.
Use `voom logs <name>` to read per-VM logs.

## Events

`voom events` streams best-effort change notifications from the cache-backed
event log. With no `--since` value it follows new events. `--since` accepts an
RFC3339 timestamp, Unix timestamp, duration such as `10m`, or an event ID.
`--until` accepts a timestamp or a duration into the future. JSON output is
JSON Lines and is flushed after every event:

```sh
voom events --output json --filter type=forward
```

Events are wake-up hints, not a state replica. A consumer must reconcile with
`voom list` or `voom forward list` when it starts and whenever it receives an
event. Delivery is not guaranteed, duplicates are possible, and concurrent
writers do not provide causal ordering. An event-ID cursor that has aged out
of the retained log produces a non-zero exit instead of silently replaying an
incomplete history.

Each JSON event has `recordType`, `schemaVersion`, opaque `id`, `time`,
`timeNano`, `type`, `action`, `actor`, and diagnostic `source` fields. Actor
attribute values are strings. Consumers must ignore unknown fields, event
types, and actions. Unsupported schema versions cause the stream to exit.

The initial event taxonomy is:

| Type | Actions | Meaning |
| --- | --- | --- |
| `vm` | `create`, `start`, `stop`, `rm` | An observed VM lifecycle operation completed; cloned VMs use `create` with a `clonedFrom` attribute. |
| `forward` | `install`, `uninstall`, `skip` | Persisted automatic-forward state changed. |

The MacOS runtime watcher emits a `stop` event when cleanup after driver exit
changes runtime state. There is no distinct crash event; Linux driver exits
are not automatically monitored.

`voom doctor` scans object directories in addition to `state.json` and reports
dangling index entries, orphaned records, duplicate names or IDs, LAN
exposure, stale process records, stale auto-forward state, and malformed guest
port reports.

## Command effects

The table below lists command effects. For MacOS, “disk” includes the Apple
platform files. `seed.img`, control-share files, and automatic-forwarding
state are generated only for Linux guests. Application forwarding, shares,
egress attachments, and passthrough are also limited to the [supported
combinations](../README.md#supported-hosts-and-guests). Disk growth and `nixos
switch` support Linux guests only.

| Command | Reads | Writes or removes |
| --- | --- | --- |
| `voom image import` | source disk and optional sidecar, or MacOS bundle | `<state>/state.json`, `<state>/images/<image-id>/image.json`, imported image disk |
| `voom image remove` | `state.json`, `image.json`, VM references | image record and disk; `state.json` entry |
| `voom create` | `state.json`, `image.json`, image disk | `state.json`, `vm.json`, VM disk copy, event log |
| `voom clone` | `state.json`, source `vm.json`, source VM disk | `state.json`, new `vm.json`, VM disk copy (no shares/forwards), event log |
| `voom start` | `state.json`, `vm.json`, `image.json`, VM disk | runtime directory, `seed.img`, control share files, sockets, process records, helper logs, runtime auto-forward state, event log |
| `voom stop` | `state.json`, `vm.json`, runtime process records | stops runtime helper processes; removes sockets, process records, and the runtime auto-forward state file; writes event log |
| `voom remove` | `state.json`, `vm.json`, runtime process records | removes VM state, VM disk, runtime directory, cache logs, and `state.json` entry; writes event log |
| `voom resources cpus` / `memory` | `state.json`, `vm.json`, runtime process record | updates stopped-VM CPU or memory allocation in `vm.json` |
| `voom resources disk grow` | `state.json`, `vm.json`, VM disk | grows the stopped VM disk |
| `voom disk reset` | `state.json`, `vm.json`, `image.json`, image disk | stages replacement boot storage and image/access metadata, then switches the active generation atomically |
| `voom forward add` / `remove` | `state.json`, `vm.json`, runtime socket when running | updates declared forwards in `vm.json`; exposes or unexposes gvproxy forwards for running VMs |
| `voom forward auto enable` / `disable` / `offset` | `state.json`, `vm.json`, image capabilities, runtime report when running | updates auto-forward settings in `vm.json`; for running VMs, removes runtime auto-forwards that no longer match the bind or offset and starts or stops the watcher, which reconciles from guest reports; writes the event log on transitions |
| `voom share add` / `remove` | `state.json`, `vm.json`, host path | updates share declarations in `vm.json`; running VMs must be stopped first |
| `voom nixos switch` | `state.json`, `vm.json`, image capabilities, flake metadata | runs `nixos-rebuild` over SSH and records switch metadata in `vm.json` |
| `voom config show` | `state.json`, `vm.json` | no state changes |
| `voom config ssh-port` | `state.json`, `vm.json`, runtime process record, host port availability | updates the stopped VM's SSH management port in `vm.json` |
| `voom gui` | `vm.json`, vfkit control socket | shows the existing MacOS display; no persistent state changes |
| `voom guest verify` | `vm.json`, image capabilities, process records, guest reports, read-only SSH probes | no state changes |
| `voom logs` | `state.json`, `vm.json`, cache log | no state changes |
| `voom doctor` | state, runtime, cache, host tools, process records | no state changes |
| `voom events` | `<cache>/events.jsonl`, retained generation | no state changes beyond creating the event lock while waiting |
