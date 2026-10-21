# Voom

![voom — magic-free virtual machines](assets/voom.png)

---

[Installation](#installation) · [Quickstarts](#getting-started) · [Guides and
reference](#guides-and-reference)

---

Voom is a CLI for running and managing Linux and MacOS virtual machines.

Voom does not run a persistent daemon and does not expose user-facing
configuration files; instead all VM-related operations are managed via the CLI.

Voom supports MacOS hosts (via [vfkit](https://github.com/crc-org/vfkit)) and
Linux hosts (via [QEMU](https://www.qemu.org/)/[KVM](https://linux-kvm.org/)):

- Linux hosts support running Linux guests
- MacOS hosts support running Linux _and_ MacOS guests

For Linux guests on either supported host OS, Voom also supports:

- Host directory shares and automatic TCP port forwarding, with per-VM port
  offsets to avoid collisions.
- [HTTP proxy attachments](docs/proxy-attachments.md). External brokers (such
  as [Agent Vault](https://docs.agent-vault.dev/) and
  [iron-proxy](https://docs.iron.sh/)) can supply credentials without storing
  them in the guest.

On Linux hosts, Voom additionally supports:

- [PCI passthrough](docs/linux-guests.md#pci-passthrough) on x86_64, for
  accessing a GPU or other PCI Express devices from the guest.
- [USB passthrough](docs/linux-guests.md#usb-passthrough), for accessing USB
  devices from the guest.

(MacOS guests work, but are not as full-featured at this point.)

## Supported hosts and guests

Voom runs guests with the same CPU architecture as the host; it does not
emulate other architectures. Windows hosts and VM execution on Intel Macs are
unsupported.

| Host                                           | Guest             | Driver   | Imported image         |
|------------------------------------------------|-------------------|----------|------------------------|
| Linux x86_64                                   | x86_64 Linux      | QEMU/KVM | `qcow2` disk           |
| Linux aarch64                                  | aarch64 Linux     | QEMU/KVM | `qcow2` disk           |
| Apple Silicon MacOS                            | aarch64 Linux     | vfkit    | `raw` disk             |
| Apple Silicon MacOS 27+ with a graphical login | aarch64 MacOS 27+ | vfkit    | Installed MacOS bundle |

All supported combinations provide image import, VM creation, start, stop,
restart, removal, clone, reset, persistent storage, ordinary guest networking,
SSH, remote command execution, and guest verification.

| Feature                                     | Linux guest on Linux | Linux guest on MacOS | MacOS guest on MacOS |
|---------------------------------------------|----------------------|----------------------|----------------------|
| Host directory shares                       | Yes¹                 | Yes¹                 | No                   |
| Manual TCP/UDP application forwards         | Yes                  | Yes                  | No                   |
| Automatic TCP forwards and listener reports | Yes¹                 | Yes¹                 | No                   |
| Explicit HTTP proxy attachments             | Yes¹                 | Yes¹                 | No                   |
| Serial console log                          | Yes²                 | Yes²                 | No                   |
| Graphical console                           | No                   | No                   | Yes                  |
| Disk growth                                 | Yes                  | Yes                  | No                   |
| `nixos switch`                              | NixOS only¹          | NixOS only¹          | No                   |
| USB passthrough                             | Yes                  | No                   | No                   |
| PCI/GPU passthrough                         | x86_64 only          | No                   | No                   |

¹ Requires the corresponding [Linux guest capabilities and
services](docs/linux-guests.md#guest-image-contract).

² `voom console` follows the serial log; it is not an interactive terminal.
The guest must be configured to write serial output.

MacOS guests use SSH for file transfer and remote commands. They do not use
Voom's reserved control share or a guest agent. Application-forwarding limits
do not affect the host port mapping used for SSH. Snapshots and suspend/resume
are not supported for either guest OS.

## Installation

> [!NOTE]
>
> Release and Nix installations include Voom's gvproxy build. Apple Silicon
> MacOS packages also include vfkit and the Apple identity helper. Other host
> dependencies, such as QEMU and SSH, are not included. See
> [host requirements](#host-requirements).

### Install a release build

Download a prebuilt archive for your platform from the [GitHub releases
page](https://github.com/mjrusso/voom/releases), verify it against
`checksums.txt`, and extract it into a directory on your `PATH`. Keep `gvproxy`
in the same directory as `voom`. On Apple Silicon MacOS, also keep `vfkit` and
`voom-macos-identity` beside `voom`.

> [!IMPORTANT]
>
> The MacOS release binaries are not notarized. If your download is quarantined,
> Gatekeeper may show _"Apple could not verify ... is free of malware"_. Clear
> the quarantine attribute from the extracted binaries before running them:
>
> ```sh
> xattr -d com.apple.quarantine ./voom ./gvproxy ./vfkit ./voom-macos-identity
> ```
>
> vfkit is ad-hoc signed with the virtualization entitlement; no Developer ID
> certificate is required.

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

In NixOS or Home Manager configs, add `voom.packages.${pkgs.system}.default`
to `environment.systemPackages` or `home.packages` after passing the flake
input to the module.

### Build from source

For instructions on building from source, see
[CONTRIBUTING.md](CONTRIBUTING.md).

### Host requirements

- **Linux**: `/dev/kvm` access through [KVM](https://linux-kvm.org/),
  [QEMU](https://www.qemu.org/) (`qemu-system-<arch>`, `qemu-img`), and
  [OpenSSH](https://www.openssh.com/) (`ssh`).
- **MacOS**: an Apple Silicon Mac with [OpenSSH](https://www.openssh.com/)
  (`ssh`, included with MacOS).
  MacOS guests additionally require MacOS 27 or later on both the host and
  guest, with a logged-in graphical session on the host; see the
  [MacOS guest guide](docs/macos-guests.md).

Optional integrations: [virtiofsd](https://gitlab.com/virtio-fs/virtiofsd)
(Linux/QEMU shares), [Nix](https://nixos.org/download/) and
[`nixos-rebuild`](https://nixos.org/manual/nixos/stable/#sec-changing-config)
(for `voom nixos switch`), and [Git](https://git-scm.com/) (NixOS switch
metadata). Note that USB passthrough from Linux hosts requires a QEMU build
with XHCI and libusb `usb-host` support, and PCI passthrough requires a QEMU
build with VFIO `vfio-pci` support.

Install any missing host tools with your OS package manager. Voom's release
archives and Nix packages already include gvproxy and, on MacOS, vfkit.

## Getting started

[Install Voom](#installation) and its [host dependencies](#host-requirements)
first. Choose the Linux quickstart below or the [MacOS
quickstart](#macos-guest).

### Linux guest

For this quickstart, download the **`generic` variant** of the official
[Debian cloud images](https://cloud.debian.org/images/cloud/trixie/), using
`raw` on Apple Silicon MacOS and `qcow2` on Linux. Voom requires cloud-init
for SSH-key injection and `--install-guest-helpers`. The `generic` image
includes it; Debian's **`nocloud` variant does not**, so it will not work for
this quickstart. Voom's NoCloud seed is a cloud-init data source, unrelated
to Debian's `nocloud` image name.

On an Apple Silicon Mac:

```bash
URL=https://cloud.debian.org/images/cloud/trixie/latest
IMG=debian-13-generic-arm64.raw
curl -fLO "$URL/$IMG"
curl -fLO "$URL/SHA512SUMS"
shasum -a 512 --ignore-missing -c SHA512SUMS
voom image import debian13 ./"$IMG" \
  --ssh-user debian --arch aarch64-linux --install-guest-helpers
```

On an x86_64 Linux host:

```bash
URL=https://cloud.debian.org/images/cloud/trixie/latest
IMG=debian-13-generic-amd64.qcow2
curl -fLO "$URL/$IMG"
curl -fLO "$URL/SHA512SUMS"
sha512sum --ignore-missing -c SHA512SUMS
voom image import debian13 ./"$IMG" \
  --ssh-user debian --arch x86_64-linux --install-guest-helpers
```

On aarch64 Linux, use `debian-13-generic-arm64.qcow2` with `--arch
aarch64-linux`.

Have an SSH key pair available before starting. Voom uses the public keys at
`~/.ssh/id_ed25519.pub`, `id_ecdsa.pub`, `id_rsa.pub`, or `id_dsa.pub`. Pass
`--ssh-identity /path/to/private-key` at import to select a specific identity
and its corresponding `.pub` file.

`--install-guest-helpers` enables shares and automatic forwarding by
installing Linux services on first boot. It requires systemd, cloud-init,
virtiofs support, and network access to install `jq`, `gawk`, and `iproute2`.
Omit the flag if you only need SSH and manual forwarding. On Linux hosts,
the helpers also require host `virtiofsd` for the control share, even without
user shares. For baked integrations and custom images, see the
[Linux guest guide](docs/linux-guests.md).

Create and start a VM:

```sh
voom create deb --image debian13
voom start deb
```

Linux VMs default to 4 vCPUs and 4096 MiB of RAM. Use `--cpus` and `--memory`
at creation to change them. `start` returns before the guest is ready for SSH;
first boot also needs time for cloud-init. Once SSH is available:

```sh
voom ssh deb -- sudo cloud-init status --wait
voom ssh deb -- uname -a
voom guest verify deb
voom ssh deb
```

To share a host directory, stop the VM and declare the share before starting
it again. The host directory must already exist:

```sh
voom stop deb
voom share add deb code ~/src/myproject /mnt/code
voom start deb
```

Once SSH is available again, check the mount:

```sh
voom ssh deb -- ls /mnt/code
```

Enable automatic forwarding for eligible TCP listeners on all guest
interfaces:

```sh
voom forward auto enable deb
voom forward list deb
```

For example, a guest server on `0.0.0.0:8080` maps to host `127.0.0.1:8080`
when that host port is available. Guest loopback listeners and SSH port 22 are
excluded. A manual forward does not require listener reports:

```sh
voom forward add deb 8080 --host-port 18080
```

See [sharing](docs/linux-guests.md#shares),
[forwarding](docs/linux-guests.md#port-forwarding), and [proxy
attachments](docs/proxy-attachments.md) for requirements and options.

### MacOS guest

Use an Apple Silicon Mac running MacOS 27 or later with a logged-in graphical
session. Start with an image bundle containing MacOS 27 or later already
installed, with SSH enabled and your public key authorized. Shut down that VM
before importing the bundle. Voom does not install MacOS. See the
[MacOS guest guide](docs/macos-guests.md) for an example builder and the bundle
format.

```sh
voom image import macos ~/vms/macos-bundle \
  --ssh-identity ~/.ssh/id_ed25519
voom create mac-dev --image macos
voom start mac-dev
```

CPU and memory default to the baked allocation. `start` opens a graphical
window and returns before SSH is ready. Once the guest has booted:

```sh
voom ssh mac-dev
voom guest verify mac-dev
```

The app uses the Voom icon and appears as `Voom: <vm name>` in the Dock and
menu bar. Closing the window keeps the VM running and removes the app from
the Dock and Command-Tab. Run `voom gui mac-dev` to restore the Dock entry and
show the window again. Quit requests guest shutdown; Force Quit terminates
the VM immediately. The [graphical session guide](docs/macos-guests.md#graphical-sessions)
describes shutdown behavior and helper cleanup.

Create, clone, and reset assign a fresh Apple machine identifier. Stop/start
preserves it. Clones copy the guest's existing users, credentials, and SSH
host keys along with its disk; Voom does not provision new accounts.

## Common operations

The following commands work for both guest OSes. Replace `dev` with your VM
name and `base` with an imported image name. Each example covers a separate
operation.

### Lifecycle and resources

```sh
voom list
voom info dev
voom restart dev
voom stop dev
voom clone dev dev-copy
voom resources cpus dev 8
voom resources memory dev 8192MiB
voom start dev
```

`stop` requests guest shutdown, then terminates remaining VM processes if
needed. Stop and restart preserve the guest disk.

Clone requires a stopped VM. CPU and memory changes, share changes, SSH port
changes, and disk growth also require a stopped VM. MacOS allocations must
meet the image's [resource
minimums](docs/macos-guests.md#identity-and-resources). Linux clones omit
shares, application forwards, automatic forwarding, USB/PCI assignments, and
proxy attachments. The command prints instructions for recreating shares,
forwards, and passthrough assignments; proxy attachments need a separate
backend for the clone.

Disk growth is available only for Linux:

```sh
voom stop dev
voom resources disk grow dev 20G
voom start dev
```

This adds 20 GiB to the disk image. Expand the guest partition and filesystem
as needed to use the additional space; Voom does not resize them.

### SSH and file transfer

Host SSH ports default to the first available port in 2222–2299 and are saved
with the VM. Use `--ssh-port` at creation or `voom config ssh-port` while
stopped to choose another port. SSH binds to host loopback by default.

Use the generated OpenSSH configuration with standard SSH tools. Replace `dev`
with your VM name; its SSH alias is `voom-dev`:

```sh
voom ssh-config dev > ./voom-dev.ssh
ssh -F ./voom-dev.ssh voom-dev
scp -F ./voom-dev.ssh ./example.txt voom-dev:
```

The VM must be running and accepting SSH connections. The `scp` example also
uses [SFTP](https://man.openbsd.org/scp), which must be enabled in the guest.
Regenerate the file after changing the VM's name, SSH port, user, or identity.
See the [SSH reference](docs/reference.md#networking-and-ssh) for key handling
and networking details.

### Reset and removal

Reset discards guest changes and replaces storage from an imported image:

```sh
voom disk reset dev --image base
voom start dev
```

Reset preserves the VM name, Voom ID, resource allocation, SSH port, and NIC
MAC, and adopts the selected image's SSH access metadata. It cannot switch
between Linux and MacOS. A MacOS reset also replaces the Apple platform files
and assigns a new Apple identity. See [storage
replacement](docs/reference.md#storage-replacement) for commit and failure
handling.

Remove a VM when it is no longer needed:

```sh
voom remove dev
```

Removal asks for confirmation; use `--force` to skip it. Removing a VM deletes
its storage, not its imported source image. VM disks are independent copies;
see [image ownership and removal](docs/reference.md#images).

## Guest verification

Use the same command for Linux and MacOS guests:

```bash
voom guest verify dev
voom guest verify dev --output json --timeout 30s
```

The command checks the driver process, SSH access, OS, architecture, SSH host
keys, and root access or passwordless sudo. It collects boot information and,
on MacOS, checks platform storage and collects hardware identity. For Linux
guests, it also checks services enabled by the image and VM configuration:
control-share mounts, share tags and access modes, recent listener reports,
and the automatic-forwarding watcher. It does not send test traffic through
forwarded ports.

Nix is optional. Verification checks installed Nix tools, the active system
link, and the daemon when detected. Missing optional components appear as
`skipped` in text and `"skipped": true` in JSON. Images with the Linux-only
`nixosSwitch` capability must pass all Nix checks.

Verification runs once and exits nonzero if a check fails. It does not wait
for the guest to boot or start missing services. `--timeout` limits the whole
run. If the VM restarts or its configuration changes during the checks,
verification fails with `runtime-consistency`; run it again against the
current VM.

JSON output includes each check's output and errors, the driver process
fingerprint, and the storage generation when present. A successful check does
not prove that restart, clone, reset, GUI, or forwarded traffic works; test
those operations separately.

## Diagnostics and recovery

Use `voom doctor` to check [host dependencies](#host-requirements), writable
directories, port availability, LAN exposure, stale process records, and state
consistency, including unsupported MacOS image capabilities and VM
declarations. On MacOS, it probes the selected vfkit for GUI lifecycle and
read-only sharing extensions and reports its resolved path. MacOS guest
startup requires the GUI lifecycle extension; read-only sharing support is
required only for Linux guests configured with read-only shares.

`voom logs <name>` defaults to the vfkit log for MacOS guests and the serial
log for Linux guests. Pass `--kind` to inspect helper logs (`qemu`, `vfkit`,
`gvproxy`, `auto-forward`, `share-mount`, `runtime-watch`).

For manual recovery, prefer command-level cleanup:

```sh
voom stop dev
voom doctor
voom remove dev --force
```

After confirming that the VM and all its helpers have stopped, you can remove
leftover files in its `<runtime>/vms/<vm-id>` directory. Removing files under
`<state>` deletes persistent state.

Lifecycle cleanup applies to all VMs, including those without egress
attachments. Voom records each helper's PID and system-specific start identity
at launch. Cleanup sends signals only when the current values match that
launch record. Start cleans up surviving helpers before launching replacement
runtime. Stop, remove, and disk reset retain process records and return an
error when identity or termination cannot be verified. Inspect the reported
PID and logs before removing retained recovery records.

## Guides and reference

- [Linux guests](docs/linux-guests.md): custom images, capability declarations,
  sharing, forwarding, USB, and PCI/GPU passthrough.
- [MacOS guests](docs/macos-guests.md): installed bundles, Apple identity,
  resource limits, graphical sessions, and reset behavior.
- [HTTP proxy attachments](docs/proxy-attachments.md): external credential
  brokers, guest configuration, security boundaries, and recovery.
- [Reference](docs/reference.md): directories, helper selection, storage,
  events, command effects, aliases, and shell completion.
- [Command reference](docs/commands/voom.md): flags and arguments for every command.

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

The [support tables](#supported-hosts-and-guests) describe which commands
apply to each host and guest. Most non-streaming commands accept `--output
json`; `ssh`, `console`, and `nixos switch` use text or subprocess output.

See the [command reference](docs/commands/voom.md) for flags and arguments,
and [aliases and shell
completion](docs/reference.md#aliases-and-shell-completion) for shortcuts and
shell setup.

## Agent skill

Voom ships agent instructions as part of the binary. To print the skill file,
run `voom skill`.

Installation is not required (just tell your agent to run `voom skill`). You
can optionally install the skill; for example:

```sh
mkdir -p ~/.agents/skills/voom
voom --skill > ~/.agents/skills/voom/SKILL.md
chmod 0644 ~/.agents/skills/voom/SKILL.md
```

## Development

For development setup, source builds, checks, generated docs, package
boundaries, and the release process, see [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Voom is released under the terms of the [MIT License](LICENSE).

Copyright (c) 2026, [Michael Russo](https://mjrusso.com).
