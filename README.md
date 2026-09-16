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

Optional features include:

- automatic host-to-guest port forwarding (with configurable per-VM offsets to
  avoid collisions) and host directory mounts
- keeping secrets out of the guest using [per-VM explicit HTTP proxy
  attachments](#explicit-http-proxy-attachments), enabling external brokers
  such as [Agent Vault](https://docs.agent-vault.dev/)
  ([source](https://github.com/Infisical/agent-vault)),
  [iron-proxy](https://docs.iron.sh/)
  ([source](https://github.com/paradigmxyz/iron-proxy)), etc. to dynamically
  supply credentials at request time

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

This command clears active runtime state (sockets and process records), but
keeps persistent state and logs.

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
> Release and Nix installations include Voom's gvproxy build. Other host
> runtime dependencies, such as QEMU, vfkit, and SSH, are not included. See
> [Host Requirements](#host-requirements).

### Install a Release Build

Download a prebuilt archive for your platform from the [GitHub releases
page](https://github.com/mjrusso/voom/releases), verify it against
`checksums.txt`, extract the `voom` binary, and place it somewhere on your
`PATH`. Keep the extracted `gvproxy` binary in the same directory as `voom`.

> [!IMPORTANT]
>
> The release binaries are not notarized by Apple, so MacOS Gatekeeper will
> quarantine the downloaded binaries. You'll see a message like _"Apple
> could not verify ... is free of malware"_. Clear the quarantine attribute
> before running it:
>
> ```sh
> xattr -d com.apple.quarantine ./voom ./gvproxy
> ```

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

Voom bundles its gvproxy build in release and Nix installations.

- **Linux**: `/dev/kvm` access through [KVM](https://linux-kvm.org/),
  [QEMU](https://www.qemu.org/) (`qemu-system-<arch>`, `qemu-img`), and
  [OpenSSH](https://www.openssh.com/) (`ssh`).
- **MacOS**: [vfkit](https://github.com/crc-org/vfkit) and
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
| Egress    | `config egress set`, `config egress clear`, `config egress enable`, `config egress disable`             |
| Shares    | `share add`, `share rm`, `share ls`                                                                     |
| NixOS     | `nixos switch`                                                                                          |
| Inspect   | `list`, `info`, `logs`, `events`, `doctor`, `guest ports`, `debug paths`, `version`, `skill`            |

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

## Explicit HTTP proxy attachments

An explicit HTTP proxy attachment connects one VM to an external proxy or
broker. The broker can apply VM-specific policy and supply credentials without
storing them in the guest. Each attachment records a private backend Unix
socket on the host:

```text
Guest application
├── uses the explicit proxy
│   └── 192.168.127.1:3128
│       └── Voom-managed listener on the guest gateway
│           └── TCP-to-Unix route
│               └── backend Unix socket assigned to this VM
│                   └── external proxy or adapter
│                       └── destination
│
└── connects directly
    └── ordinary direct networking
        └── destination
```

Voom manages the fixed listener on the guest gateway and the TCP-to-Unix route.
The external proxy or its adapter creates and listens on the backend Unix
socket. Voom also publishes `/run/voom/egress.json` and an optional CA bundle at
`/run/voom/egress-ca.pem`. Guest tooling can read the manifest to configure
applications. Voom does not run or manage the external proxy.

Guest applications must explicitly use the published proxy address. Voom does
not set `HTTP_PROXY`, `HTTPS_PROXY`, or application proxy settings. It also does
not block direct network access. Voom publishes the optional CA bundle but does
not install it. HTTP and HTTPS clients use the same proxy address. HTTPS clients
use HTTP CONNECT through that address.

Attachments require an image with `controlShare` capability. Check the imported
image before you configure the attachment:

```sh
voom image inspect <image>
```

The output must show `capabilities: controlShare=true`. Images imported with
`--install-guest-helpers` have this capability. For custom images, see the
[Guest Image Contract](#guest-image-contract). Before you set or enable an
attachment, start the external proxy so its backend socket accepts connections.

```sh
voom config egress set agent-a \
  --backend-socket /run/credential-proxy/vm-01JXYZ.sock \
  --ca-cert /etc/credential-proxy/ca.pem
voom start agent-a
```

Inside the guest, an application can use the fixed proxy address directly:

```sh
curl --proxy http://192.168.127.1:3128 \
  --cacert /run/voom/egress-ca.pem \
  https://example.com/
```

The `--ca-cert` option publishes a CA bundle for proxies that terminate TLS.
Omit `--ca-cert` and `--cacert` when the proxy does not terminate TLS. Use
`voom config egress disable` and `voom config egress enable` to change proxy
access while the VM runs. To remove the saved attachment, stop the VM and use
`voom config egress clear`.

For attachment states, external-manager integration, guest files, security
requirements, and recovery behavior, see [Egress proxy
integration](#egress-proxy-integration).

Example integrations:

- [Agent Vault on
  NixOS](https://github.com/mjrusso/nixos-config#voom-agent-vault) provides a
  complete Voom integration with per-VM identity, host-only proxy tokens, CA
  setup, and guest wrappers.
- [iron-proxy's CONNECT tunnel
  guide](https://docs.iron.sh/guides/socks5-connect) and [example
  configuration](https://github.com/paradigmxyz/iron-proxy/blob/main/iron-proxy.example.yaml)
  provide a starting point for explicit proxying, credential injection, and CA
  setup. Bridge its TCP tunnel listener to a private Unix socket before
  attaching it to Voom.

## Agent Skill

Voom ships agent instructions as part of the binary. To print the skill file,
run `voom skill`.

Installation is not required (just tell your agent to run `voom skill`). You
can optionally install the skill; for example:

```sh
mkdir -p ~/.agents/skills/voom
voom --skill > ~/.agents/skills/voom/SKILL.md
chmod 0644 ~/.agents/skills/voom/SKILL.md
```

## Diagnostics and Recovery

Use `voom doctor` to check system dependencies (as per [Host
Requirements](#host-requirements)), as well as writable directories, port
availability, LAN exposure, stale process records, and state consistency.

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

Lifecycle cleanup applies to all VMs, including those without egress attachments.
Voom records each helper's PID and system-specific start identity at launch.
Cleanup sends signals only when the current values match that launch record.
Start cleans up surviving helpers before launching replacement runtime. Stop,
remove, and disk reset retain process records and return an error when identity
or termination cannot be verified. Inspect the reported PID and logs before
removing retained recovery records.

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
build images, bundle hypervisors, or run a background control plane. Release
and Nix installations include Voom's gvproxy build.

Images must be explicitly imported, VMs must be created explicitly (referencing
an existing, already-imported image), and optional guest integrations are
always opt-in.

### Directories

| Purpose                     | Default (XDG)                                  | Override           |
|-----------------------------|------------------------------------------------|--------------------|
| Configuration               | `$XDG_CONFIG_HOME/voom` or `~/.config/voom`    | `VOOM_CONFIG_DIR`  |
| State (source of truth)     | `$XDG_DATA_HOME/voom` or `~/.local/share/voom` | `VOOM_STATE_DIR`   |
| Cache (logs)                | `$XDG_CACHE_HOME/voom` or `~/.cache/voom`      | `VOOM_CACHE_DIR`   |
| Runtime (sockets, process records) | `$XDG_RUNTIME_DIR/voom` or `/tmp/voom-$UID` | `VOOM_RUNTIME_DIR` |

Run `voom debug paths` to inspect resolved values. Voom uses a `gvproxy` binary
next to its own executable before searching `PATH`. External helpers can be
overridden with `VOOM_GVPROXY`, `VOOM_VIRTIOFSD`, and
`VOOM_QEMU_AARCH64_UEFI`.

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
interval. The host treats a report older than 30 seconds as stale. A missing,
stale, or malformed report leaves installed runtime auto-forwards in place. A
valid report is authoritative: Voom removes auto-forwards for listeners that
the report does not list, and a valid empty report removes all of them. Manual
forwards are never affected.

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
- changing the bind or offset removes runtime auto-forwards that no longer
  match immediately, without waiting for a guest report; forwards for the new
  bind or offset appear after the next valid report.

`voom forward discover <name>` is an audit/preview command; `voom forward ls`
shows effective rows including skipped auto-forwards with an explanation.

### Egress proxy integration

#### Attachment state

This table shows the expected healthy state. `voom info` reports the observed
runtime state and any difference from the saved attachment.

| Saved attachment | VM state           | Private listener | Published manifest |
|------------------|--------------------|------------------|--------------------|
| None or disabled | Stopped or running | No               | No                 |
| Enabled          | Stopped            | No               | No                 |
| Enabled          | Running            | Yes              | Yes                |

Set and clear require a stopped VM with no surviving runtime. Enable and disable
also work while the VM runs. Run enable again to repair runtime drift. If the
runtime already matches the saved attachment, enable preserves the listeners,
tunnels, and runtime files. Disable closes the private listener, pending dials,
and active tunnels. Disable cannot retract requests that a broker already
accepted or guarantee cancellation of upstream work.

#### External managers

External attachment managers should pass the immutable VM ID with every change.
They should first save the attachment in the disabled state:

```sh
voom info agent-a
VM_ID=30BD3BFA3D7C382195F82F603B5D4F11
voom config egress set agent-a \
  --expect-id "$VM_ID" \
  --disabled \
  --backend-socket "/run/credential-proxy/$VM_ID.sock" \
  --ca-cert /etc/voom-proxy/ca.pem
voom config egress enable agent-a --expect-id "$VM_ID"
```

`--expect-id` is available on set, clear, enable, and disable. Voom checks it
while it holds the VM state lock. Thus, a replacement VM with the same name
cannot receive the old attachment. `set --disabled` validates and reserves the
socket without publishing a guest route. Voom also validates the CA bundle. The
manager can then complete its policy checks and confirm that no emergency hold
applies before it enables the attachment.

Assign each backend socket and broker policy to the immutable VM ID shown by
`voom info`, not the mutable VM name. Voom reserves a socket across stopped and
disabled VMs in its state store. Operators must enforce uniqueness across other
state stores, users, and clients. Rename preserves attachments. Clones omit them
and report the new VM ID for a separate backend.

#### Security boundaries

The external proxy or broker, not Voom, controls authentication, credential
injection, and policy enforcement. Proxy use is advisory, and direct networking
remains available. Requests that bypass the proxy receive no broker-injected
credentials.

Keep socket directories and broker policy under trusted host control. A shared
unauthenticated listener for all sockets does not preserve VM identity. Guest
headers and manifest contents do not authenticate a VM.

#### Guest files and CA bundles

Voom publishes these files through `voom-control` when it enables an attachment:

- `/run/voom/egress.json`: schema version 1, mode `explicit`, and `httpProxy` and
  `httpsProxy` set to `http://192.168.127.1:3128`.
- `/run/voom/egress-ca.pem`: optional validated public certificate bundle. The
  manifest includes `caCertificate` only when this file is configured.

Host copies live under `<runtime VM directory>/control/`. Files have mode 0644.
Voom publishes the manifest after the CA file. Voom replaces the files
separately, so the update is not atomic. The metadata document always advertises
`controlShare.egressPath`, including when the saved configuration has no
attachment. Guests must choose how to consume the manifest and install the
public CA.

CA bundles must contain only X.509 certificate PEM blocks and at least one CA
certificate. Voom rejects private keys, other PEM blocks, and extraneous data.
A bundle can contain a maximum of 4 MiB. Each operation publishes the exact
bytes that it validated. `info` and `doctor` compare the published CA to the
current source. After the source changes, run enable or restart to update the
guest files.

#### Observation and recovery

The `egress-config` column of `voom list` shows the saved attachment (`enabled`,
`disabled`, or `none`) and does not check the running VM. `voom info` reports
observed route state, connection count when available, and configuration drift.
`voom doctor` uses local socket probes and host API queries. `voom doctor` sends
no proxy requests or outbound internet traffic. Disabled declarations can start
without their backend, certificate, or private-transport capability.

Disable persists the disabled declaration after removing runtime access. A
crash or persistence failure between those steps can leave the saved attachment
enabled. A later restart can restore access.

A failed enable can leave an enabled declaration with runtime access removed.
After you fix the reported cause, run enable again. If Voom cannot confirm route
removal, it tries to stop the VM runtime. Unconfirmed termination is an error.
Voom retains the process record and log paths for recovery.

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

Runtime files are disposable. Notable paths under `<runtime>/vms/<vm-id>/`:

- `vm.process.json`, `gvproxy.process.json`, `auto-forward.process.json`,
  `virtiofs-<tag>.process.json` — process IDs and system-specific start
  identities
- `seed.img` — regenerated cloud-init NoCloud disk (`meta-data`, `user-data`,
  `network-config`)
- `control/mounts.json`, `control/ports.json` — host side of the reserved
  `voom-control` share
- `network.sock`, `qemu.mon`, `vfkit.sock`, `virtiofs-<tag>.sock` — driver and
  helper sockets

Cache holds disposable logs: `serial.log`, `qemu.log` or `vfkit.log`,
`gvproxy.log`, `auto-forward.log`, `share-mount.log`, and the global
`events.jsonl` event stream. The stream retains one rotated generation at
`events.jsonl.1` and coordinates writers through `events.lock`. Use `voom logs
<name>` to read per-VM logs.

### Events

`voom events` streams best-effort change notifications from the cache-backed
event log. With no `--since` value it follows new events. `--since` accepts an
RFC3339 timestamp, Unix timestamp, duration such as `10m`, or an event ID.
`--until` accepts a timestamp or a duration into the future. JSON output is
JSON Lines and is flushed after every event:

```sh
voom events --output json --filter type=forward
```

Events are wake-up hints, not a state replica. A consumer must reconcile with
`voom list` or `voom forward ls` when it starts and whenever it receives an
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

Events for an unexpected VM process exit are not emitted in this version.

`voom doctor` scans object directories in addition to `state.json` and reports
dangling index entries, orphaned records, duplicate names or IDs, LAN exposure,
stale process records, stale auto-forward state, and malformed guest port reports.

Common command effects:

| Command | Reads | Writes or removes |
| --- | --- | --- |
| `voom image import` | source disk, optional sidecar metadata | `<state>/state.json`, `<state>/images/<image-id>/image.json`, imported image disk |
| `voom image rm` | `state.json`, `image.json`, VM references | image record and disk; `state.json` entry |
| `voom create` | `state.json`, `image.json`, image disk | `state.json`, `vm.json`, VM disk copy, event log |
| `voom clone` | `state.json`, source `vm.json`, source VM disk | `state.json`, new `vm.json`, VM disk copy (no shares/forwards), event log |
| `voom start` | `state.json`, `vm.json`, `image.json`, VM disk | runtime directory, `seed.img`, control share files, sockets, process records, helper logs, runtime auto-forward state, event log |
| `voom stop` | `state.json`, `vm.json`, runtime process records | stops runtime helper processes; removes sockets, process records, and the runtime auto-forward state file; writes event log |
| `voom rm` | `state.json`, `vm.json`, runtime process records | removes VM state, VM disk, runtime directory, cache logs, and `state.json` entry; writes event log |
| `voom resources cpus` / `memory` | `state.json`, `vm.json`, runtime process record | updates stopped-VM CPU or memory allocation in `vm.json` |
| `voom resources disk grow` | `state.json`, `vm.json`, VM disk | grows the stopped VM disk |
| `voom disk reset` | `state.json`, `vm.json`, `image.json`, image disk | replaces the VM disk and updates the VM image/access metadata |
| `voom forward add` / `rm` | `state.json`, `vm.json`, runtime socket when running | updates declared forwards in `vm.json`; exposes or unexposes gvproxy forwards for running VMs |
| `voom forward auto enable` / `disable` / `offset` | `state.json`, `vm.json`, image capabilities, runtime report when running | updates auto-forward settings in `vm.json`; for running VMs, removes runtime auto-forwards that no longer match the bind or offset and starts or stops the watcher, which reconciles from guest reports; writes the event log on transitions |
| `voom share add` / `rm` | `state.json`, `vm.json`, host path | updates share declarations in `vm.json`; running VMs must be stopped first |
| `voom nixos switch` | `state.json`, `vm.json`, image capabilities, flake metadata | runs `nixos-rebuild` over SSH and records switch metadata in `vm.json` |
| `voom config show` | `state.json`, `vm.json` | no state changes |
| `voom config ssh-port` | `state.json`, `vm.json`, runtime process record, host port availability | updates the stopped VM's SSH management port in `vm.json` |
| `voom logs` | `state.json`, `vm.json`, cache log | no state changes |
| `voom doctor` | state, runtime, cache, host tools, process records | no state changes |
| `voom events` | `<cache>/events.jsonl`, retained generation | no state changes beyond creating the event lock while waiting |

## Development

For development setup, source builds, checks, generated docs, package
boundaries, and the release process, see [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Voom is released under the terms of the [MIT License](LICENSE).

Copyright (c) 2026, [Michael Russo](https://mjrusso.com).
