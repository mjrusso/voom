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

[Install Voom](#installation) and its [host dependencies](#host-requirements),
then choose a guest OS:

- [Run a Linux guest](docs/linux-guests.md#getting-started): download and import
  a cloud image, start a VM, verify it, and configure shares and forwarding.
- [Run a MacOS guest](docs/macos-guests.md): prepare an installed image bundle,
  start a VM, verify it, and use the graphical console.

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

Clone requires a stopped VM. CPU and memory changes, share changes, and SSH
port changes also require a stopped VM. MacOS allocations must meet the
recorded [resource minimums](docs/macos-guests.md#identity-and-resources).
Linux clones omit shares, application forwards, automatic forwarding,
USB/PCI assignments, and proxy attachments. The command prints instructions for recreating shares,
forwards, and passthrough assignments; proxy attachments need a separate
backend for the clone.

See [disk growth](docs/linux-guests.md#disk-growth) for expanding a Linux
guest disk.

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

## Guides and reference

- [Linux guests](docs/linux-guests.md): getting started, verification, custom
  images, sharing, forwarding, USB, and PCI/GPU passthrough.
- [MacOS guests](docs/macos-guests.md): getting started, verification, installed
  bundles, graphical sessions, Apple identity, and resource limits.
- [Diagnostics and recovery](docs/diagnostics-and-recovery.md): host checks,
  logs, verification failures, and runtime cleanup.
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

See [Third-party licenses](docs/third-party-licenses.md) for bundled components
and the locations of their license notices.

Copyright (c) 2026, [Michael Russo](https://mjrusso.com).
