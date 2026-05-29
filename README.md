# Voom

![voom — magic-free virtual machines](assets/voom.png)

**`voom`**: *run magic-free local VMs.*

---

Voom is CLI for running and managing Linux-based virtual machines. Voom
supports MacOS hosts (via [vfkit](https://github.com/crc-org/vfkit)) and Linux
hosts (via [QEMU](https://www.qemu.org/)/[KVM](https://linux-kvm.org/)).

Voom is deliberately simple and not magical. In particular, Voom has:

- no daemon
- no user-edited config files
- all runtime state lives under `$XDG_*` paths
- explicit VM lifecycle: `import → create → start`

Voom also optionally supports automatic port forwarding from host-to-guest
(with configurable per-VM offsets to avoid collisions), and host directory
mounting. These advanced features require opt-in via a sidecar file; see [Guest
Image Contract](#guest-image-contract) for full details.

Note that there are many excellent tools in this space, with differing goals
and trade-offs. [Kevin Lynagh](https://kevinlynagh.com/)'s
[Vibe](https://github.com/lynaghk/vibe/) is one such example for Mac users —
see its [list of alternatives](https://github.com/lynaghk/vibe/#alternatives)
for a primer on available options.

Voom might be a nice choice for you if you have the need for disposable-ish,
pseudo-ephemeral VMs (great for letting your agents `--yolo`, among plenty of
other uses).

> [!TIP]
>
> The author uses Voom with a custom image built from his [NixOS system
> configuration](https://github.com/mjrusso/nixos-config): the same Nix Flake
> that defines the host machine also produces VM images with tools
> pre-installed and configured, so it's possible to SSH in, launch tmux, Emacs,
> and coding agents, and get to work immediately. Because the image ships with
> [nix-direnv](https://github.com/nix-community/nix-direnv), and his projects
> generally use Nix Flakes to define per-project toolchains, per-project
> environments load automatically with no additional setup.
>
> If you would like to replicate a similar setup, see this reference
> implementation:
> [module](https://github.com/mjrusso/nixos-config/blob/main/hosts/container/default.nix)
> (wires up the control-share mount, the `voom-portfwd` and `voom-mount-shares`
> helpers, and their systemd units), and [build
> script](https://github.com/mjrusso/nixos-config/blob/main/scripts/bake-golden)
> (bakes the disk image with matching Voom sidecar).

## Tour

> [!NOTE]
>
> The demo below assumes you already have a compatible image (`./image.raw`, in
> this example) with a sibling `image.meta.json` sidecar. The [Quick
> Start](#quick-start) shows an example of how to acquire a pre-built image,
> although Voom particularly shines when you [bring your
> own](https://github.com/mjrusso/voom#guest-image-contract).

**Create and run a VM** on your host machine:

```sh
voom image import base ./image.raw  --meta ./image.meta.json
voom create dev --image base
voom start dev
voom ssh dev
```

_Note that this example assumes that a VM image named `image.raw` is available,
with a sibling `image.meta.json` sidecar. (More below on how to create an
image, or use an existing one.)_

**Mount a host directory** into the guest as a `virtio-fs` share:

```sh
voom share add dev code ~/src/myproject /mnt/code
voom restart dev
voom ssh dev -- ls /mnt/code
```

**Auto-forward** so that anything the guest binds on `0.0.0.0` shows up on
`127.0.0.1` on the host, with no per-port declaration needed:

```sh
voom forward auto enable dev
voom ssh dev -- 'nohup python3 -m http.server 8080 >/dev/null 2>&1 &'
sleep 3
voom forward ls dev
curl http://127.0.0.1:8080
```

**Stop the VM** when you're done using it. Runtime state (sockets, pidfiles,
helper logs) is cleared, but persistent state (disk, declared shares and
forwards, switch metadata) remains:

```sh
voom stop dev
```

## Quick Start

Install `voom` (see [Installation](#installation), below). Make sure that the
host runtime tools from [Host Requirements](#host-requirements) are installed
on your platform: vfkit + gvproxy are required on MacOS hosts, and QEMU + KVM +
gvproxy are required on Linux hosts.

Voom works with off-the-shelf images. The following examples use [a Debian
cloud image](https://cloud.debian.org/images/cloud/trixie).

> [!NOTE]
>
> For off-the-shelf images with `cloud-init`, specify `--install-guest-helpers`
> at import time to enable the full set of Voom features.
>
> For Debian, always use the **`generic`** variant. The similarly-named
> **`nocloud`** variant ships without `cloud-init` and boots straight to a root
> prompt, ignoring Voom's seed install. The `genericcloud` variant should work,
> but has a reduced kernel driver set; prefer `generic` unless image size
> matters.
>
> Note that the `--install-guest-helpers` flag, used below, makes Voom's
> `NoCloud` seed install `voom-portfwd` and the share-mount service on first
> boot (and thus sets `controlShare`, `guestPortReport`, and `guestShareMount`
> capabilities to true). Drop the flag for the minimum contract (boot + SSH +
> manual forwards only). For images that already ship the helpers (e.g. a baked
> NixOS image), pass `--meta sidecar.json` instead of
> `--install-guest-helpers`. _(Sidenote: the terminology is confusing; Voom's
> `NoCloud` `cloud-init` seed is unrelated to Debian's `nocloud` variant.)_

If running on a MacOS (Apple Silicon) host, acquire a Debian cloud image and
import into Voom:

```bash
BUILD=20260518-2482
URL=https://cloud.debian.org/images/cloud/trixie/$BUILD
IMG=debian-13-generic-arm64-$BUILD.raw

curl -fLO "$URL/$IMG"
curl -fLO "$URL/SHA512SUMS"
shasum -a 512 --ignore-missing -c SHA512SUMS

voom image import debian13 ./"$IMG" --ssh-user debian --arch aarch64-linux --install-guest-helpers
```

If running on a Linux host (this example is x86_64), acquire a Debian cloud
image and import into Voom:

```bash
BUILD=20260518-2482
URL=https://cloud.debian.org/images/cloud/trixie/$BUILD
IMG=debian-13-generic-amd64-$BUILD.qcow2

curl -fLO "$URL/$IMG"
curl -fLO "$URL/SHA512SUMS"
sha512sum --ignore-missing -c SHA512SUMS

voom image import debian13 ./"$IMG" --ssh-user debian --arch x86_64-linux --install-guest-helpers
```

Once an image is imported, the commands are generally the same regardless of
host OS and CPU architecture:

```bash
# Create VM
voom create deb --image debian13
voom start deb

# Wait for cloud-init (first boot only)
until voom ssh deb -- 'systemctl is-active voom-portfwd.service' 2>/dev/null \
  | grep -q '^active$'; do sleep 5; done

voom ssh deb
voom forward auto enable deb
voom share add deb code /path/to/project /mnt/code
voom restart deb  # pick up the new share
voom ssh deb -- ls /mnt/code
```

Most non-streaming commands accept `--output json` for automation (`list`,
`info`, `image inspect`, `version`, `doctor`, `debug paths`, forward, share,
disk, resources, lifecycle). Streaming commands (`ssh`, `console`, `nixos
switch`) are text/subprocess oriented.

For more information, see the generated command reference
([docs/commands](docs/commands/)).

## Installation

> [!NOTE]
>
> Installation does **not** include host runtime dependencies, such as QEMU,
> vfkit, gvproxy, SSH, etc. See [Host Requirements](#host-requirements).

### Install with Go

```sh
go install github.com/mjrusso/voom/cmd/voom@latest
```

### Install with Nix


```sh
nix profile install github:mjrusso/voom
```

Or run directly without installing:

```sh
nix run github:mjrusso/voom -- version
```

To use `voom` from another flake:

```nix
{
  inputs.voom.url = "github:mjrusso/voom";

  outputs = { nixpkgs, voom, ... }:
    let
      system = "x86_64-linux";
      pkgs = import nixpkgs { inherit system; };
    in {
      devShells.${system}.default = pkgs.mkShell {
        packages = [ voom.packages.${system}.default ];
      };
    };
}
```

In NixOS or Home Manager configs, add `voom.packages.${pkgs.system}.default` to
`environment.systemPackages` or `home.packages` after passing the flake input
to the module.

### Build from Source

For instructions on building from source, see
[CONTRIBUTING.md](CONTRIBUTING.md).

## Host Requirements

Voom does not bundle runtime dependencies.

- **Linux**: `/dev/kvm` access through [KVM](https://linux-kvm.org/),
  [QEMU](https://www.qemu.org/) (`qemu-system-<arch>`, `qemu-img`),
  [gvproxy](https://github.com/containers/gvisor-tap-vsock), and
  [OpenSSH](https://www.openssh.com/) (`ssh`).
- **MacOS**: [vfkit](https://github.com/crc-org/vfkit),
  [gvproxy](https://github.com/containers/gvisor-tap-vsock), and
  [OpenSSH](https://www.openssh.com/) (`ssh`). Note that only Apple Silicon is
  supported.

Optional integrations: [virtiofsd](https://gitlab.com/virtio-fs/virtiofsd)
(Linux/QEMU shares), [Nix](https://nixos.org/download/) and
[`nixos-rebuild`](https://nixos.org/manual/nixos/stable/#sec-changing-config)
(for `voom nixos switch`), and [Git](https://git-scm.com/) (NixOS switch
metadata).

Install host runtime tools manually, or with your OS package manager.

On `aarch64` QEMU hosts, Voom looks for UEFI firmware in this order:

```text
/usr/share/AAVMF/AAVMF_CODE.fd
/usr/share/qemu/edk2-aarch64-code.fd
/usr/share/edk2/aarch64/QEMU_EFI.fd
```

Set `VOOM_QEMU_AARCH64_UEFI=/path/to/firmware.fd` when your distribution stores
firmware elsewhere. On macOS, vfkit uses direct boot metadata from the imported
image or a bootable disk image.

| Host OS | Host arch       | Image system    | Driver | Disk format |
|---------|-----------------|-----------------|--------|-------------|
| Linux   | x86_64          | `x86_64-linux`  | qemu   | qcow2       |
| Linux   | aarch64         | `aarch64-linux` | qemu   | qcow2       |
| Darwin  | arm64 / aarch64 | `aarch64-linux` | vfkit  | raw         |

## Common Workflows

**Lifecycle.** `voom create` makes a VM; `voom start` boots it. (Note that
`voom start` will never implicitly create a VM.) `voom stop` shuts the VM down
but keeps persistent state; `voom rm <name> --force` removes a VM's state,
disk, runtime files, and cache logs. New VMs default to 4 CPUs and `4096MiB`
memory; adjust at creation with `--cpus`, `--memory`, `--ssh-port`, and
`--start`. For stopped VMs, update CPU and RAM allocations with `voom
resources cpus <name> <n>` and `voom resources memory <name> <size>`; the new
values apply the next time the VM starts.

**SSH.** `voom ssh <name>` opens an interactive shell. Trailing arguments pass
through to `ssh` as a one-shot remote command: for example, `voom ssh <name> --
uname -a` runs `uname -a` in the VM and exits.

**Forwards.** Manual forwards bind `127.0.0.1` by default. `--lan` or `--bind
0.0.0.0` exposes to other machines (recorded in `vm.json`; `voom doctor` warns
about LAN exposure). `--auto` picks a free host port. `voom forward auto enable
<name>` opts into runtime auto-forwarding for images that declare
`guestPortReport`. SSH ports are allocated from 2222–2299 and persisted. `voom
ssh-config <name>` prints an OpenSSH host block; `voom forward ls` shows SSH,
manual, and runtime auto-forward rows across VMs. `voom forward auto enable
<name> --offset 10000` shifts every auto-forwarded host port by a fixed amount,
so multiple VMs can auto-forward the same guest ports without colliding on the
host.

**Shares.** `voom share add <name> <tag> <host-path> <guest-path>` declares a
virtio-fs share. Appending `--ro`/`--readonly` makes the share read-only. The
tag `voom-control` is reserved. Shares are mounted by the guest from
`/run/voom/mounts.json` through the reserved control share. On QEMU, Voom
exports each share with a `virtiofsd` helper; on vfkit, voom attaches native
vfkit virtio-fs devices. Adding or removing shares requires the VM to be
stopped.

**Disk.** `voom disk grow <name>` defaults to `10G` and only acts on stopped
VMs. `voom disk reset <name>` stops the VM and replaces its disk from the image
while preserving VM configuration. Sizes accept
`M`/`MB`/`MiB`/`G`/`GB`/`GiB`/`T`/`TB`/`TiB`; bare numbers are rejected.

**NixOS switch.** `voom nixos switch <name>` runs `nixos-rebuild --target-host`
over the VM's persisted SSH port, sets `NIX_SSHOPTS` with the VM's SSH options,
and records `switchedAt`, `flakeRef`, and the current git revision in `vm.json`
on success. Failures distinguish SSH, privilege, missing Nix/NixOS tooling, and
rebuild errors. Only supported on NixOS guests.

## Diagnostics And Recovery

Use `voom doctor` to check system dependencies (as per [Host
Requirements](#host-requirements)), as well as writable directories, port
availability, LAN exposure, stale pidfiles, and state consistency.

`voom logs <name>` reads the serial log by default; pass `--kind` to inspect
helper logs (`qemu`, `vfkit`, `gvproxy`, `auto-forward`, `share-mount`).

For manual recovery, prefer command-level cleanup:

```sh
voom stop <name>
voom doctor
voom rm <name> --force
```

If a VM is already stopped and only runtime debris remains, it is safe to
remove that VM's `<runtime>/vms/<vm-id>` directory. Removing files under
`<state>` is destructive and should be handled with care.

## Reference

Voom manages local development VMs through the host's VM stack. It does not
build images, bundle QEMU/vfkit/gvproxy, or run a background control plane.
Images are imported explicitly, VMs are created explicitly, and optional guest
integrations are gated by image capabilities.

### Directories

| Purpose                     | Default (XDG)                                  | Override           |
|-----------------------------|------------------------------------------------|--------------------|
| Configuration               | `$XDG_CONFIG_HOME/voom` or `~/.config/voom`    | `VOOM_CONFIG_DIR`  |
| State (source of truth)     | `$XDG_DATA_HOME/voom` or `~/.local/share/voom` | `VOOM_STATE_DIR`   |
| Cache (logs)                | `$XDG_CACHE_HOME/voom` or `~/.cache/voom`      | `VOOM_CACHE_DIR`   |
| Runtime (sockets, pidfiles) | `$XDG_RUNTIME_DIR/voom` or `/tmp/voom-$UID`    | `VOOM_RUNTIME_DIR` |

Run `voom debug paths` to inspect resolved values. External helpers can be
overridden with `VOOM_GVPROXY`, `VOOM_VIRTIOFSD`, and `VOOM_QEMU_AARCH64_UEFI`.

Setting all four `VOOM_*` overrides to disposable directories is the supported
way to experiment without touching your normal state. This is useful for
testing, scratch work, or locally developing Voom (as per details in
[CONTRIBUTING.md](CONTRIBUTING.md)).

### Images

**Names and IDs.** VM and image names are mutable labels (matching
`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`), decoupled from the stable generated IDs that
Voom uses for internal bookkeeping.

**Disks.** Each VM disk is an independent copy of the image disk. `voom image
rm` blocks while any VM references the image, unless `--force` is used; a
forced removal leaves existing VM disks in place with their historical
metadata.

**SSH login user.** Required at import: set it through the sidecar `user` field
or the `--ssh-user` flag (the flag wins if both are present). On start, Voom
writes a `NoCloud` seed authorizing the image's `sshIdentityPath` public key,
or, when unset, the readable defaults `~/.ssh/id_ed25519.pub`, `id_ecdsa.pub`,
`id_rsa.pub`, and `id_dsa.pub`. The seed grants passwordless sudo to root, and
to the login user when the two differ.

**Sidecar metadata.** A sidecar records an image's properties (`system`,
`format`, login `user`, and so on) and declares which guest integrations are
available:

- `controlShare` — guest can mount the reserved `voom-control` share at
  `/run/voom`
- `guestPortReport` — guest runs `voom-portfwd` and writes
  `/run/voom/ports.json`
- `guestShareMount` — guest can mount declared host shares
- `nixosSwitch` — guest supports `voom nixos switch`

A typical sidecar:

```json
{
  "user": "root",
  "nixosTargetUser": "root",
  "system": "x86_64-linux",
  "format": "raw",
  "flake_rev": "abcdef123456",
  "baked_at": "2026-05-21T00:00:00Z",
  "capabilities": {
    "controlShare": true,
    "guestPortReport": true,
    "guestShareMount": true,
    "nixosSwitch": true
  }
}
```

Note:

- `nixosTargetUser` (or `nixos_target_user`) overrides the user `voom nixos
  switch` targets, defaulting to `user`.

- Setting `"installGuestHelpers": true` is equivalent to passing
  `--install-guest-helpers` on import.

**NixOS images.** The useful pattern is to have your flake build image outputs
for the systems and formats Voom can run, then write a sibling sidecar
declaring at least `user`, `system`, `format`, `baked_at`, `flake_rev`, and the
capabilities the image actually ships. The author's NixOS config does this with
outputs named `.#images.<system>.<format>`; see
[scripts/bake-golden](https://github.com/mjrusso/nixos-config/blob/main/scripts/bake-golden)
for a reference implementation.

### Guest Image Contract

There are two tiers of compatible images:

**Minimum tier** supports `start`, `stop`, `restart`, `ssh`, `console`, manual
`forward`, `logs`, `info`, `list`. The image must:

- boot on the recorded host architecture;
- use the disk format the driver requires (`qcow2` for QEMU, `raw` for vfkit);
- run sshd on TCP 22 and accept the configured SSH identity;
- network through gvproxy DHCP/user-mode (no bridged or root-privileged
  setups).

**Full-feature tier** additionally supports shares, auto-forwarding, and `voom
nixos switch` (when applicable). It must also provide `ss` (or equivalent),
`mount` / `mountpoint` / `umount`, virtiofs, the `voom-portfwd` helper, a
service that mounts shares from `/run/voom/mounts.json`, passwordless privilege
escalation when the NixOS target user is not root, and a conventional
ACPI/vfkit shutdown path.

For NAT-backed Voom images, leave the guest firewall disabled. The guest sits
behind gvproxy NAT, so no traffic can reach the guest except through explicit
host-side forwards, which are managed by Voom. Use a stricter guest firewall
only when attaching to a real network (bridged, macvtap, VPN). Recommended
NixOS setting:

#### `voom-portfwd` Contract

The helper writes `/run/voom/ports.json` atomically (tmp-and-rename) every
interval. The host treats reports older than ~2× the scan interval as stale;
stale or malformed data removes runtime auto-forwards while leaving manual
forwards intact.

```json
{
  "schemaVersion": 1,
  "generatedAt": "2026-05-20T12:00:00Z",
  "listeners": [
    {
      "proto": "tcp",
      "addr": "0.0.0.0",
      "port": 8080,
      "pid": 1234,
      "process": "python3"
    }
  ]
}
```

Field rules: `schemaVersion` must be integer `1`; `generatedAt` must be an
RFC 3339 timestamp in UTC; `proto` must be `"tcp"` for v1; `pid` and `process`
are optional.

Recommended systemd shape:

```text
voom-portfwd.service
  after: network-online.target
  wants: network-online.target
  exec: voom-portfwd --output /run/voom/ports.json --interval 2s
```

For reference, see the author's NixOS config at
[`hosts/container/default.nix`](https://github.com/mjrusso/nixos-config/blob/main/hosts/container/default.nix)
(helper script, systemd unit, `voom-mount-shares` service, `/run/voom` virtiofs
control-share mount).

### Networking Details

Bind addresses must be IP literals; hostnames including `localhost` are
rejected. Voom treats overlapping binds as conflicts:

- `0.0.0.0:p` conflicts with every IPv4 bind on port `p`.
- A specific IPv4 conflicts with `0.0.0.0` and with itself on the same port.
- `::` conflicts with every IPv6 bind on that port and conservatively also with
  IPv4 binds on the same port unless the host proves it can hold both.
- `::1` and other specific IPv6 addresses conflict with `::` and with
  themselves on the same port.

A persisted SSH or forward port that is occupied at start time fails rather
than silently reallocating.

Auto-forwarding is runtime state, not a manual forward declaration. The guest
helper writes `/run/voom/ports.json` through the reserved control share; Voom
plans host forwards from fresh reports and records installed/skipped rows in
`<runtime>/vms/<vm-id>/auto-forwards.json`. Rules:

- only TCP listeners are considered;
- guest port `22` is reserved for SSH;
- guest listeners must bind all guest interfaces (`0.0.0.0`, `::`, or `*`);
- runtime auto-forwards bind `127.0.0.1` on the host;
- host port = guest port + configured auto-forward offset.

`voom forward discover <name>` is an audit/preview command; `voom forward ls`
shows effective rows including skipped auto-forwards with an explanation.

### State And Runtime Layout

State is the persistent source of truth. JSON files are written atomically via
temp-file-and-rename; partial files are ignored on load.

```text
<state>/state.json                          # name → ID index
<state>/images/<image-id>/image.json        # metadata, capabilities
<state>/images/<image-id>/disk.qcow2|raw    # imported image disk
<state>/vms/<vm-id>/vm.json                 # config, forwards, shares
<state>/vms/<vm-id>/disk.qcow2|raw          # per-VM disk copy
<state>/locks/...                           # state and per-VM locks
```

Runtime files are process-owned and disposable, and are removed when a VM
stops. Notable paths under `<runtime>/vms/<vm-id>/`:

- `vm.pid`, `gvproxy.pid`, `auto-forward.pid`, `virtiofs-<tag>.pid` — pidfiles
  validated against process command before voom signals them
- `seed.img` — regenerated cloud-init NoCloud disk (`meta-data`, `user-data`,
  `network-config`)
- `control/mounts.json`, `control/ports.json` — host side of the reserved
  `voom-control` share
- `network.sock`, `qemu.mon`, `vfkit.sock`, `virtiofs-<tag>.sock` — driver and
  helper sockets

Cache holds disposable logs: `serial.log`, `qemu.log` or `vfkit.log`,
`gvproxy.log`, `auto-forward.log`, `share-mount.log`. Use `voom logs <name>` to
read.

`voom doctor` scans object directories in addition to `state.json` and reports
dangling index entries, orphaned records, duplicate names or IDs, LAN exposure,
stale pidfiles, stale auto-forward state, and malformed guest port reports.

Common command effects:

| Command | Reads | Writes or removes |
| --- | --- | --- |
| `voom image import` | source disk, optional sidecar metadata | `<state>/state.json`, `<state>/images/<image-id>/image.json`, imported image disk |
| `voom image rm` | `state.json`, `image.json`, VM references | image record and disk; `state.json` entry |
| `voom create` | `state.json`, `image.json`, image disk | `state.json`, `vm.json`, VM disk copy |
| `voom start` | `state.json`, `vm.json`, `image.json`, VM disk | runtime directory, `seed.img`, control share files, sockets, pidfiles, helper logs, runtime auto-forward state |
| `voom stop` | `state.json`, `vm.json`, runtime pidfiles | stops runtime helper processes; removes sockets, pidfiles, and the runtime auto-forward state file |
| `voom rm` | `state.json`, `vm.json`, runtime pidfiles | removes VM state, VM disk, runtime directory, cache logs, and `state.json` entry |
| `voom resources cpus` / `memory` | `state.json`, `vm.json`, runtime pidfile | updates stopped-VM CPU or memory allocation in `vm.json` |
| `voom disk grow` | `state.json`, `vm.json`, VM disk | grows the stopped VM disk |
| `voom disk reset` | `state.json`, `vm.json`, `image.json`, image disk | replaces the VM disk and updates the VM image/access metadata |
| `voom forward add` / `rm` | `state.json`, `vm.json`, runtime socket when running | updates declared forwards in `vm.json`; exposes or unexposes gvproxy forwards for running VMs |
| `voom forward auto enable` / `disable` / `offset` | `state.json`, `vm.json`, image capabilities, runtime report when running | updates auto-forward settings in `vm.json`; reconciles or removes runtime auto-forwards for running VMs |
| `voom share add` / `rm` | `state.json`, `vm.json`, host path | updates share declarations in `vm.json`; running VMs must be stopped first |
| `voom nixos switch` | `state.json`, `vm.json`, image capabilities, flake metadata | runs `nixos-rebuild` over SSH and records switch metadata in `vm.json` |
| `voom logs` | `state.json`, `vm.json`, cache log | no state changes |
| `voom doctor` | state, runtime, cache, host tools, pidfiles | no state changes |

## Development

For development setup, source builds, checks, generated docs, package
boundaries, and the release process, see [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Voom is released under the terms of the [MIT License](LICENSE).

Copyright (c) 2026, [Michael Russo](https://mjrusso.com).
