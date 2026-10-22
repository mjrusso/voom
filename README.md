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

MacOS guests are not yet as full-featured as Linux guests.

Note that all guests support manual port forwarding (TCP and UDP).

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
| Manual TCP/UDP application forwards         | Yes                  | Yes                  | Yes                  |
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

_MacOS guests use SSH for file transfer and remote commands; they do not use
Voom's reserved control share, or a guest agent. Manual forwards do not require
guest services. Snapshots and suspend/resume are not supported for either guest
OS._

## Installation

Download a prebuilt archive from [GitHub releases](https://github.com/mjrusso/voom/releases)
or install with Nix:

```sh
nix profile install github:mjrusso/voom
```

See the [installation guide](docs/installation.md) for host requirements,
release setup, and Nix integration.

## Getting started

[Install Voom](docs/installation.md), then choose a guest OS:

- [Run a Linux guest](docs/linux-guests.md#getting-started): download and import
  a cloud image, start a VM, verify it, and configure shares and forwarding.
- [Run a MacOS guest](docs/macos-guests.md): prepare an installed image bundle,
  start a VM, verify it, and use the graphical console.

## Common operations

These examples work for both Linux and MacOS guests. Replace `dev` with your VM
name.

### Inspect VMs

List your VMs, then inspect one for its status, resource allocation, and SSH
connection details:

```sh
voom list
voom info dev
```

### Stop or restart a VM

Stop a VM when you have finished using it:

```sh
voom stop dev
```

Voom requests guest shutdown, then terminates remaining VM processes if
needed. The guest disk is preserved. Run `voom start dev` to start it again,
or `voom restart dev` to stop and start it in one command.

### Clone a VM

Stop the source VM, then copy it into a new VM named `dev-copy`:

```sh
voom stop dev
voom clone dev dev-copy
voom start dev-copy
```

The clone has its own disk containing the source's current guest state,
including installed software, user accounts, credentials, and SSH host keys.
It keeps the source's CPU and memory allocation but gets its own Voom ID,
SSH port, and NIC MAC address. MacOS clones also get a new
[Apple machine identifier](docs/macos-guests.md#identity-and-resources).

For Linux and MacOS guests, manual forwards are not copied. For Linux guests,
host directory shares, automatic-forwarding settings, USB/PCI assignments, and
proxy attachments are also not copied; however, the clone command prints
commands that you can manually invoke to recreate shares, forwards, and device
assignments. A proxy attachment needs a separate backend for the clone; see
[proxy attachments](docs/proxy-attachments.md).

### Change CPU or memory allocation

Stop the VM before changing its allocation. This example sets 8 vCPUs and
8192 MiB of RAM:

```sh
voom stop dev
voom resources cpus dev 8
voom resources memory dev 8192MiB
voom start dev
```

MacOS allocations must meet the recorded
[resource minimums](docs/macos-guests.md#identity-and-resources).
For expanding a Linux guest disk, see [disk growth](docs/linux-guests.md#disk-growth).

### SSH and file transfer

By default, Voom assigns each VM the first available host SSH port in the
2222–2299 range and saves that port with the VM. Use `--ssh-port` at creation
or `voom config ssh-port` while stopped to choose another port. SSH binds to
host loopback by default.

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

### Remove a VM

Remove a VM when it is no longer needed:

```sh
voom remove dev
```

Removal asks for confirmation; use `--force` to skip it. Removing a VM deletes
its storage, not its imported source image. VM disks are independent copies;
see [image ownership and removal](docs/reference.md#images).

To rebuild an existing VM from an image, see reset instructions for
[Linux](docs/linux-guests.md#reset-a-linux-guest) or
[MacOS](docs/macos-guests.md#reset-a-macos-guest).

## Guides and reference

- [Installation](docs/installation.md): release builds, Nix, and host requirements.
- [Linux guests](docs/linux-guests.md): getting started, verification, custom
  images, sharing, forwarding, USB, and PCI/GPU passthrough.
- [MacOS guests](docs/macos-guests.md): getting started, verification, installed
  bundles, graphical sessions, Apple identity, and resource limits.
- [Diagnostics and recovery](docs/diagnostics-and-recovery.md): host checks,
  logs, verification failures, and runtime cleanup.
- [HTTP proxy attachments](docs/proxy-attachments.md): external credential
  brokers, guest configuration, security boundaries, and recovery.
- [Reference](docs/reference.md): command overview, directories, helper
  selection, storage, events, command effects, aliases, and shell completion.
- [Command reference](docs/commands/voom.md): flags and arguments for every command.

Use `voom --help` to list commands and `voom <command> --help` for a
command's usage and options.

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
