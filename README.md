# Voom

![voom — magic-free virtual machines](assets/voom.png)

**`voom`**: *run magic-free local VMs.*

---

Voom is a CLI for running and managing Linux-based virtual machines. Voom
supports MacOS hosts (via [vfkit](https://github.com/crc-org/vfkit)) and Linux
hosts (via [QEMU](https://www.qemu.org/)/[KVM](https://linux-kvm.org/)).

Voom is deliberately simple and not magical. In particular, Voom has:

- no daemon
- no user-edited config files
- all runtime state lives under `$XDG_*` paths
- explicit VM lifecycle: `import → create → start → stop`

Voom optionally supports automatic host-to-guest port forwarding (with
configurable per-VM offsets to avoid collisions) and host directory mounting.

Note that there are many excellent tools in this space, with differing goals
and trade-offs. [Kevin Lynagh](https://kevinlynagh.com/)'s
[Vibe](https://github.com/lynaghk/vibe/) is one such example for Mac users; see
its [list of alternatives](https://github.com/lynaghk/vibe/#alternatives) for a
primer on available options.

Voom might be a nice choice for you if you have the need for disposable-ish,
pseudo-ephemeral VMs (great for letting your agents `--yolo`, among plenty of
other uses).

## Getting Started

Voom works with off-the-shelf cloud images, as well as custom images that you
build yourself (see [Custom Images](#custom-images)).

The following examples use official [Debian cloud
images](https://cloud.debian.org/images/cloud/trixie), in conjunction with the
`--install-guest-helpers` flag (provided as an argument to `voom image
import`). The `--install-guest-helpers` flag installs Voom's guest helpers when
the VM boots for the first time, which enable host directory shares and
automatic port forwarding. (If you don't want host directory shares or
automatic port forwarding, you can omit the flag when importing an image.)

<details>
<summary>Choosing a Debian image variant</summary>

Use the **`generic`** variant. The similarly-named **`nocloud`** variant ships
without `cloud-init` and boots straight to a root prompt, ignoring Voom's seed
(so `--install-guest-helpers` and SSH-key injection will silently have no
effect). The `genericcloud` variant should work but ships a reduced kernel
driver set; prefer `generic` unless image size matters.

_Terminology note:_ Voom's `cloud-init` seed uses a **`NoCloud`** datasource,
which is unrelated to Debian's **`nocloud`** image variant.
</details>

### 0. Install prerequisites

[Install Voom](#installation), and all required [host runtime
dependencies](#host-requirements).

### 1. Import an image

On an Apple Silicon Mac (`aarch64`), use a `raw` disk image:

```bash
BUILD=20260518-2482
URL=https://cloud.debian.org/images/cloud/trixie/$BUILD
IMG=debian-13-generic-arm64-$BUILD.raw

curl -fLO "$URL/$IMG"
curl -fLO "$URL/SHA512SUMS"
shasum -a 512 --ignore-missing -c SHA512SUMS

voom image import debian13 ./"$IMG" --ssh-user debian --arch aarch64-linux --install-guest-helpers
```

On a Linux host (this example is `x86_64`), use a `qcow2` disk image:

```bash
BUILD=20260518-2482
URL=https://cloud.debian.org/images/cloud/trixie/$BUILD
IMG=debian-13-generic-amd64-$BUILD.qcow2

curl -fLO "$URL/$IMG"
curl -fLO "$URL/SHA512SUMS"
sha512sum --ignore-missing -c SHA512SUMS

voom image import debian13 ./"$IMG" --ssh-user debian --arch x86_64-linux --install-guest-helpers
```

`aarch64` Linux hosts are also supported.

> [!TIP]
>
> Always use `raw` disk images on Mac hosts, and `qcow2` disk images on Linux
> hosts.

### 2. Create a VM

Create a new VM (called `deb`, in this example), based on the `debian13` image
that was imported in the previous step:

```bash
voom create deb --image debian13
```

New VMs are allocated 4 vCPUs and 4 GiB (4096 MiB) of RAM by default. You can
override these values at VM creation time by passing the `--cpus` / `--memory`
flags.

### 3. Mount a host directory

Declare a host directory as a `virtio-fs` share:

```bash
voom share add deb code ~/src/myproject /mnt/code
```

Shares can only be added or removed while the VM is stopped; this host
directory (`~/src/myproject`) will mount automatically on boot (at `/mnt/code`
on the guest).

### 4. Start and connect

```bash
voom start deb

# Wait for first-boot cloud-init to install the guest helpers
until voom ssh deb -- 'systemctl is-active voom-portfwd.service' 2>/dev/null \
  | grep -q '^active$'; do sleep 5; done

voom ssh deb -- ls /mnt/code   # one-shot command

voom ssh deb                   # interactive shell
```

### 5. Forward guest ports

When auto-forwarding is enabled, anything the guest binds on `0.0.0.0` is
available at `127.0.0.1` on the host by default:

```bash
voom forward auto enable deb
voom ssh deb -- 'nohup python3 -m http.server 8080 >/dev/null 2>&1 &'
sleep 3
voom forward ls deb
curl http://127.0.0.1:8080
```

If you're running multiple VMs that each bind the same guest ports, you can
assign each VM an offset to prevent host collisions. For example, `voom forward
auto enable deb --offset 10000` maps host `18080` to guest `8080` (`host port =
guest port + offset`). To adjust an existing offset, run `voom forward auto
offset deb <n>`.

To expose auto-forwards beyond loopback, pass `--bind <ip>` or `--lan` when
enabling auto-forwarding. For example, `--bind 100.x.y.z` listens on that local
host address, while `--lan` listens on `0.0.0.0`.

If you would prefer to explicitly expose ports:

```bash
voom ssh deb -- 'nohup python3 -m http.server 9090 >/dev/null 2>&1 &'
voom forward add deb 9090      # host 127.0.0.1:9090 -> guest 9090
sleep 3
curl http://127.0.0.1:9090
```

The host port defaults to the guest port and binds to `127.0.0.1`; pass
`--host-port` to map to a different host port, or `--lan` to expose on
`0.0.0.0`.

### 6. Shut down the VM

Run `voom stop` to shut down a running VM:

```bash
voom stop deb
```

This command clears runtime state (sockets, pidfiles, helper logs), but keeps
persistent state (disk, declared shares and forwards).

### 7. Make adjustments to a stopped VM

Certain changes can only be made while the VM is stopped:

```bash
voom resources cpus deb 8           # set the vCPU count
voom resources memory deb 8192MiB   # set the RAM allocation
voom resources disk grow deb 20G    # grow the disk by 20G
```

Note that the disk will be resized immediately, but updated CPU and memory
allocations will not take effect until the next time the VM is started.

## Installation

> [!NOTE]
>
> Installation does **not** include host runtime dependencies, such as QEMU,
> vfkit, gvproxy, SSH, etc. See [Host Requirements](#host-requirements).

### Install a Release Build

Download a prebuilt archive for your platform from the [GitHub releases
page](https://github.com/mjrusso/voom/releases), verify it against
`checksums.txt`, extract the `voom` binary, and place it somewhere on your
`PATH`.

> [!IMPORTANT]
>
> The release binaries are not notarized by Apple, so MacOS Gatekeeper will
> quarantine the downloaded `voom` binary. You'll see a message like _"Apple
> could not verify ... is free of malware"_. Clear the quarantine attribute
> before running it:
>
> ```sh
> xattr -d com.apple.quarantine ./voom
> ```

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
firmware elsewhere. On MacOS, vfkit uses direct boot metadata from the imported
image or a bootable disk image.

| Host OS | Host arch       | Image system    | Driver | Disk format |
|---------|-----------------|-----------------|--------|-------------|
| Linux   | x86_64          | `x86_64-linux`  | qemu   | qcow2       |
| Linux   | aarch64         | `aarch64-linux` | qemu   | qcow2       |
| Darwin  | arm64 / aarch64 | `aarch64-linux` | vfkit  | raw         |

## Commands Overview

| Area      | Commands                                                                                                |
|-----------|---------------------------------------------------------------------------------------------------------|
| Lifecycle | `create`, `start`, `stop`, `restart`, `rm`, `clone`, `rename`                                           |
| Images    | `image import`, `image inspect`, `image list`, `image rm`                                               |
| Compute   | `resources cpus`, `resources memory`                                                                    |
| Disk      | `resources disk grow`, `disk reset`                                                                     |
| Access    | `ssh`, `console`, `ssh-config`, `config show`, `config ssh-port`                                        |
| Forwards  | `forward add`, `forward rm`, `forward ls`, `forward discover`, `forward auto enable`/`disable`/`offset` |
| Shares    | `share add`, `share rm`, `share ls`                                                                     |
| NixOS     | `nixos switch`                                                                                          |
| Inspect   | `list`, `info`, `logs`, `doctor`, `guest ports`, `debug paths`, `version`                               |

Notes and considerations:

- Host SSH ports are auto-allocated (in the range 2222–2299); the selection is
  persisted as part of the VM metadata. You can set an arbitrary port at VM
  creation time with the `--ssh-port` flag, and existing port allocations can
  be changed later by running the `voom config ssh-port` command.

- A VM must be stopped before resizing, changing shares, growing its disk
  (`voom resources disk grow`), or changing its SSH port.

- Most non-streaming commands accept `--output json` to simplify automation;
  streaming commands (`ssh`, `console`, `nixos switch`) are instead
  text/subprocess oriented.

_For the full command reference, see [docs/commands](docs/commands/)._

## Diagnostics and Recovery

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

## Custom Images

Voom's guest integrations can be configured to be installed on first boot by
passing the `--install-guest-helpers` flag on image import. Alternatively, they
can be baked into a custom-built image and paired with a *sidecar* (a small
JSON file that declares the image's capabilities).

`--install-guest-helpers` is simpler than baking a custom image, but there are
some trade-offs and differences worth acknowledging:

|                           | `--install-guest-helpers`                                    | Bake into the image (+ sidecar)                  |
|---------------------------|--------------------------------------------------------------|--------------------------------------------------|
| **Works with**            | any `cloud-init`-capable image (e.g. stock Debian `generic`) | images you build yourself                        |
| **First-boot cost**       | one-time install on first boot                               | none: helpers are already present in the image   |
| **Network at first boot** | required (installs `jq`/`gawk`/`iproute2` from distro repo)  | not required                                     |
| **Enables**               | shares + auto port-forward                                   | shares + auto port-forward + `voom nixos switch` |
| **Setup**                 | one flag at image import                                     | a build pipeline                                 |

When passing `--install-guest-helpers`, Voom generates a `cloud-init` `NoCloud`
seed that writes Voom's helpers (`voom-portfwd`, the share-mount service, the
`/run/voom` control mount) and enables them on first boot. Note that the
install runs once, with subsequent starts reusing the helpers already on disk.
Alternatively, baking the helpers into the image avoids first-boot
latency and the package-install network dependency.

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

See [Guest Image Contract](#guest-image-contract) for details on what a
full-featured image must provide, and [Images](#images) for the sidecar format
specification.

---

## Reference

Voom manages local development VMs through the host's VM stack. Voom does not
build images, bundle dependencies (QEMU/vfkit/gvproxy/etc.), or run a
background control plane.

Images must be explicitly imported, VMs must be created explicitly (referencing
an existing, already-imported image), and optional guest integrations are
always opt-in.

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

**NixOS images.** The recommended pattern is to have your flake build image
outputs for the systems and formats Voom can run, then write a sibling sidecar
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
only when attaching to a real network (bridged, macvtap, VPN).

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
- runtime auto-forwards bind `127.0.0.1` on the host by default; pass
  `--bind <ip>` or `--lan` to expose them elsewhere;
- host port = guest port + configured auto-forward offset.

`voom forward discover <name>` is an audit/preview command; `voom forward ls`
shows effective rows including skipped auto-forwards with an explanation.

### Shares

`voom share add <name> <tag> <host-path> <guest-path>` declares a `virtio-fs`
share in `vm.json`; append `--ro`/`--readonly` for a read-only mount. The tag
`voom-control` is reserved. On QEMU, Voom exports each share through a
`virtiofsd` helper; on vfkit, it attaches native virtio-fs devices. The guest
mounts declared shares from `/run/voom/mounts.json` (delivered over the reserved
control share), so the image must ship the full-feature `guestShareMount`
capability. Adding or removing a share requires the VM to be stopped.

### State and Runtime Layout

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
| `voom clone` | `state.json`, source `vm.json`, source VM disk | `state.json`, new `vm.json`, VM disk copy (no shares/forwards) |
| `voom start` | `state.json`, `vm.json`, `image.json`, VM disk | runtime directory, `seed.img`, control share files, sockets, pidfiles, helper logs, runtime auto-forward state |
| `voom stop` | `state.json`, `vm.json`, runtime pidfiles | stops runtime helper processes; removes sockets, pidfiles, and the runtime auto-forward state file |
| `voom rm` | `state.json`, `vm.json`, runtime pidfiles | removes VM state, VM disk, runtime directory, cache logs, and `state.json` entry |
| `voom resources cpus` / `memory` | `state.json`, `vm.json`, runtime pidfile | updates stopped-VM CPU or memory allocation in `vm.json` |
| `voom resources disk grow` | `state.json`, `vm.json`, VM disk | grows the stopped VM disk |
| `voom disk reset` | `state.json`, `vm.json`, `image.json`, image disk | replaces the VM disk and updates the VM image/access metadata |
| `voom forward add` / `rm` | `state.json`, `vm.json`, runtime socket when running | updates declared forwards in `vm.json`; exposes or unexposes gvproxy forwards for running VMs |
| `voom forward auto enable` / `disable` / `offset` | `state.json`, `vm.json`, image capabilities, runtime report when running | updates auto-forward settings in `vm.json`; reconciles or removes runtime auto-forwards for running VMs |
| `voom share add` / `rm` | `state.json`, `vm.json`, host path | updates share declarations in `vm.json`; running VMs must be stopped first |
| `voom nixos switch` | `state.json`, `vm.json`, image capabilities, flake metadata | runs `nixos-rebuild` over SSH and records switch metadata in `vm.json` |
| `voom config show` | `state.json`, `vm.json` | no state changes |
| `voom config ssh-port` | `state.json`, `vm.json`, runtime pidfile, host port availability | updates the stopped VM's SSH management port in `vm.json` |
| `voom logs` | `state.json`, `vm.json`, cache log | no state changes |
| `voom doctor` | state, runtime, cache, host tools, pidfiles | no state changes |

## Development

For development setup, source builds, checks, generated docs, package
boundaries, and the release process, see [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Voom is released under the terms of the [MIT License](LICENSE).

Copyright (c) 2026, [Michael Russo](https://mjrusso.com).
