# Voom

![voom — magic-free virtual machines](assets/voom.png)

**`voom`**: *run magic-free local VMs.*

---

Voom is a CLI for running and managing Linux and MacOS virtual machines. Voom
supports MacOS hosts (via [vfkit](https://github.com/crc-org/vfkit)) and Linux
hosts (via [QEMU](https://www.qemu.org/)/[KVM](https://linux-kvm.org/)).

Voom has no daemon or user-edited configuration files. Commands manage VM
state, logs, and runtime files in per-user directories. The basic workflow is
`import → create → start → stop`.

Voom also supports:

- Host directory shares and automatic TCP port forwarding, with per-VM port
  offsets to avoid collisions.
- [HTTP proxy attachments](#explicit-http-proxy-attachments) for keeping
  credentials on the host. External brokers such as
  [Agent Vault](https://docs.agent-vault.dev/)
  ([source](https://github.com/Infisical/agent-vault)) and
  [iron-proxy](https://docs.iron.sh/)
  ([source](https://github.com/paradigmxyz/iron-proxy)) can supply credentials
  when the guest makes a request.
- [USB passthrough](#usb-passthrough) and [PCI passthrough](#pci-passthrough)
  for QEMU VMs on Linux.

Voom is intended for local development and disposable VMs. For other options,
see [Vibe's list of VM tools](https://github.com/lynaghk/vibe/#alternatives).

## Getting Started

This quickstart runs a Linux guest on Linux or an Apple Silicon Mac. Use an
off-the-shelf cloud image or a [custom Linux image](#custom-images). For MacOS
guests, see [MacOS Guests](#macos-guests).

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

Linux VMs default to 4 vCPUs and 4 GiB (4096 MiB) of RAM. Override these values
with `--cpus` and `--memory` when creating the VM. MacOS VMs default to the
allocation recorded in their image.

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
voom forward list deb
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
`0.0.0.0`. Use `--bind <host-ip>` to listen on a specific local IP address.
TCP is the default; use `--protocol udp` for UDP.

```bash
voom forward add deb 8080 --protocol tcp --host-port 18080 --bind 127.0.0.1
voom forward add deb 5353 --protocol udp --host-port 15353 --bind 127.0.0.1
voom forward list deb
voom forward remove deb 15353 --protocol udp --bind 127.0.0.1
```

TCP and UDP can share a host address and port. Forwards survive VM restarts.
Removing a forward closes only that protocol's listener. UDP forwards return
replies to each client and require the gvproxy bundled with Voom. Automatic
forwarding discovers TCP listeners only. If a forwarding request has an unknown
outcome, Voom stops the VM before releasing its reservation. If gvproxy
termination cannot be confirmed, Voom retains the reservation and returns an error.
Unconfirmed new automatic forwards appear in `voom forward list` as `reservation`
rows with `uncertain` status.

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

## Guest Verification

Use the same command for Linux and MacOS guests:

```bash
voom guest verify dev
voom guest verify dev --output json --timeout 30s
```

The command checks the driver process, SSH access, OS, architecture, SSH host
keys, and root access or passwordless sudo. It collects boot information and,
on MacOS, hardware identity. It also checks the services enabled by the image
and VM configuration: control-share mounts, share tags and access modes, recent
listener reports, and the automatic-forwarding watcher. It does not send test
traffic through forwarded ports.

Nix is optional. Verification checks installed Nix tools, the active system
link, and the daemon when detected. Missing optional components appear as
`skipped` in text and `"skipped": true` in JSON. Images with the Linux-only
`nixosSwitch` capability must pass all Nix checks. MacOS updates through
nixos-config's `voom-update` use SSH instead.

Verification runs once and exits nonzero if a check fails. It does not wait for
the guest to boot or start missing services. `--timeout` limits the whole run.
If the VM restarts or its configuration changes during the checks, verification
fails with `runtime-consistency`; run it again against the current VM.

JSON output includes each check's output and errors, the driver process
fingerprint, and the storage generation when present. A successful check does
not prove that restart, clone, reset, GUI, or forwarded traffic works; test
those operations separately.

## MacOS Guests

MacOS guests require an Apple Silicon Mac running MacOS 27 or later and a logged-in
graphical session. Voom includes its patched vfkit 0.6.4 build in MacOS release
archives and Nix packages; a separate vfkit installation is unnecessary.

### Bake an image

Build an image with
[`nixos-config`'s MacOS image builder](https://github.com/mjrusso/nixos-config#macos-vm-images).
It installs from an Apple restore image (IPSW), configures the guest, and exports
the complete
bundle Voom imports. Building requires Nix and an Xcode toolchain with the
MacOS 27 SDK.
Running the finished image with Voom requires neither Xcode nor the builder.

1. Set up a personal configuration flake using nixos-config's
   [system configuration template](https://github.com/mjrusso/nixos-config/tree/main/templates/system-config).
2. Configure its `macosImage` output with pinned Apple IPSW and Nix distribution
   URLs and SHA-256 hashes, the installed SDK version, and the Darwin target.
   The [builder instructions](https://github.com/mjrusso/nixos-config#macos-vm-images)
   include the complete recipe and setup details.
3. From that configuration flake's directory, validate the recipe and bake:

   ```sh
   nix run .#bake-macos -- --check
   nix run .#bake-macos -- --identity ~/.ssh/id_ed25519
   ```

The private key must work without prompting, directly or through ssh-agent;
the builder reads the adjacent `.pub` file and authorizes that public key in the
guest. A successful bake validates a standalone boot and shuts the guest down,
leaving `~/vms/golden-aarch64-darwin` by default. Use `--output PATH` to choose
another bundle directory. Keep the disk and Apple platform files together.

This builder installs Nix, nix-darwin, Home Manager, and Voom's guest services.
Voom can also run MacOS images without Nix. Other builders must produce the
[image bundle format](#image-bundle-format) below.

### Import and run

Import the completed, stopped bundle using the SSH key authorized during baking:

```bash
voom image import macos ~/vms/golden-aarch64-darwin \
  --ssh-identity ~/.ssh/id_ed25519
voom create mac-dev --image macos
voom start mac-dev
# Once the guest has booted and SSH is available:
voom guest verify mac-dev
voom ssh mac-dev
voom stop mac-dev
voom clone mac-dev mac-copy
```

`--ssh-identity` takes the private key whose public key is already authorized
in the guest. Voom records the host path to that key. It does not copy the key
or change the guest's SSH access. MacOS SSH connections ignore host SSH
configuration.

`start` returns when vfkit's control socket is ready. The guest may still be
booting; wait for SSH before running `guest verify`. Voom uses gvproxy to
forward SSH to `192.168.127.3:22` in the guest. The builder's standalone vfkit
boot uses different networking and does not test this connection.

### Image bundle format

The source bundle must contain nonempty regular files named `image.json`,
`restore.json`, `toolchain.json`, `disk.raw`, `HardwareModel`,
`MachineIdentifier`, and `AuxiliaryStorage`. Import copies these files while
holding disk and auxiliary-storage locks, then validates the copied files and
builds the image record from them. The source manifest is preserved as
`bundle.json`. After import, the source bundle can be moved or removed.

The source `image.json` must declare `schemaVersion: 1`,
`system: "aarch64-darwin"`, `format: "macos-bundle"`, and positive `cpuCount`,
`memoryMiB`, and `diskGiB` values. Set `user` to the login account, or supply
`--ssh-user` at import. `restore.json` records `version` (27 or later) and `build`;
`toolchain.json` records the tools used to build the image. The bundle
directory and its required files cannot be symlinks.

Capabilities describe services already installed in the image. `metadataDisk`
and `nixosSwitch` must remain false; `guestShareMount` and `guestPortReport` each
require `controlShare`. Bundles with all capabilities false remain supported.
The `--install-guest-helpers` import flag installs Linux helpers and is rejected
for MacOS bundles.

### Identity and resources

Create, clone, and reset copy the installed guest state and assign a fresh
Apple machine identifier. Clone requires a stopped source VM. Restarts retain
the identifier. Clones also receive their own Voom IDs, SSH ports, and NIC MAC
addresses. Guest hostnames, SSH host keys, users, and credentials remain those
in the copied disk.
When checking identity across boots, compare the Apple machine identifier,
platform UUID, or serial number. `provisioning_UDID` can change between boots.

CPU and memory default to the baked allocation. Each bundle must record Apple's
`minimumCPUCount` and `minimumMemorySize` (bytes) in `restore.json`. Both must
be positive and no larger than the baked allocation. Voom allows smaller
allocations down to those minimums, rounding memory up to whole MiB. Disk
capacity comes from the image.

`info`, `image inspect`, and `doctor` report platform state and source MacOS
version/build. Those version fields describe the image and do not track
subsequent guest OS upgrades.

### Graphical sessions

`voom start` opens a window titled with the VM name. Closing the window hides
it and keeps the VM running. Click its Dock icon or run `voom gui mac-dev` to
show the same display again. `gui` requires a running MacOS VM.

Quit (Command-Q or the Dock menu) requests guest shutdown and keeps the app
responsive while shutdown completes. If the guest refuses shutdown or remains
running after 60 seconds, vfkit shows a message and leaves the VM running.
Force Quit terminates the VM immediately. A Voom background watcher cleans up
network helpers after confirmed vfkit exit, including after Force Quit or a crash.
If process inspection fails, the watcher retains the helpers and reports the error
in the `runtime-watch` log.

The process name and Dock icon belong to vfkit; the window title uses the VM
name at startup. Restart a VM to use an updated vfkit build.
MacOS guests require Voom's patched vfkit. An incompatible `VOOM_VFKIT` override
is rejected before starting helpers.

GUI login uses the guest account's password, which Voom does not manage.
If the image builder generated a password you do not know, connect with `voom ssh`
and run `sudo /usr/bin/passwd "$(id -un)"` inside the guest to set one.

### Shares and automatic forwarding

Images built with nixos-config's Darwin VM configuration include a launchd
service that mounts Voom's `voom-control` share at `/var/run/voom`, mounts user
shares, and publishes TCP listeners every two seconds. The image must advertise
`controlShare`, `guestShareMount`, and `guestPortReport` only when it provides
those services. Installing Voom does not install guest services; build and
import an image that includes them.

```sh
voom stop mac-dev
voom share add mac-dev project "$HOME/project" /Volumes/project
voom share add mac-dev reference "$HOME/reference" /Volumes/reference --readonly
voom forward auto enable mac-dev
voom start mac-dev
voom guest ports mac-dev
voom forward list mac-dev
```

Choose writable guest mount locations such as `/Volumes/project` or a directory
under `/Users`; MacOS's sealed root cannot create arbitrary `/mnt` directories.
Share changes require a stopped VM. Read-only shares are enforced by Voom's
patched vfkit on the host and requested read-only in the guest. A conflicting
existing mount is reported in `/var/log/voom-guest-services.log` inside the guest.

Automatic forwarding follows the [port conflict and offset rules](#networking-details).
Wildcard TCP listeners (`*`, `0.0.0.0`, or `::`) are eligible; SSH and listeners
bound only to guest loopback are excluded. Start services on `0.0.0.0` to expose
them. Reports are published atomically; a failed listener scan does not publish
an empty report. Reset rejects replacement images lacking capabilities needed
by retained shares or automatic forwarding before stopping the VM.

### Reset a MacOS guest

```bash
voom disk reset mac-dev --image macos
voom start mac-dev
# Once the guest has booted and SSH is available:
voom guest verify mac-dev
```

Reset stops the VM and replaces its disk, hardware model, auxiliary storage,
and Apple machine identifier from the selected image. It discards guest changes
and generates a fresh Apple identity. The VM name, Voom ID, CPU and memory
allocation, SSH port, NIC MAC, shares, and forwarding settings stay the same.
An image that cannot support those resources or configured services is rejected
before shutdown. Reset cannot change a VM between Linux and MacOS.

Reset can repair missing boot files, provided the source image is complete and
the old runtime has released its storage locks.

On both Linux and MacOS, reset copies replacement storage into a new directory,
then saves the VM record to select that copy and its image/access metadata.
A failure before that save leaves the old storage selected, though the VM may
already be stopped. If the save's outcome is uncertain, Voom keeps both copies.
The next successful start or reset uses the saved record to remove unused
storage. Errors during cleanup explicitly say that reset already committed.

Stop, reset, and removal keep the VM's records when Voom cannot confirm that
the runtime has exited. On MacOS, these commands also wait for
Virtualization.framework to release the disk and auxiliary-storage locks.

### Storage compatibility

New VM records on both platforms and MacOS image records use schema 2. New VMs keep
their boot files in a storage directory selected by the VM record. Existing
Linux VMs remain readable with schema 1 and move to schema 2 on reset. Linux
image records remain schema 1.
These versions describe Voom's stored records; the source bundle's `image.json`
manifest still uses schema 1.

Older released binaries cannot read schema 2 records and may fail to list VMs
or images when those records are present.

### Test a baked image with Voom

nixos-config provides an optional `verify-macos-voom` command that tests a baked
bundle through a selected Voom package. It imports a disposable image, creates a
VM, checks guest health, writable and read-only shares, and automatic TCP
forwarding. It repeats the checks after restart, then removes the test VM and
imported image.

The command clears helper overrides and verifies `voom debug helpers` resolves
vfkit, gvproxy, and the identity helper beside the selected Voom executable.
It saves capabilities, binary hashes, and command output. Test state lives in
a new `/tmp` directory; the source bundle must be stopped. If shutdown fails,
the command keeps the VM and its state so you can recover it. A graphical
login session and an SSH key usable without prompting are required.

Run from the nixos-config checkout, using a Voom checkout with MacOS guest support:

```sh
nix build path:../voom#voom --out-link /tmp/voom-under-test
nix run path:.#verify-macos-voom -- \
  --voom /tmp/voom-under-test/bin/voom \
  --bundle "$HOME/vms/golden-aarch64-darwin" \
  --identity "$HOME/.ssh/id_ed25519"
```

Use `--work-dir /tmp/voom-check-01` to choose a new test directory and
`--timeout SECONDS` to change the per-stage limit (default 600). Logs and
`evidence/result.json` are retained on success and failure. The standalone
vfkit boot during baking does not replace this test of Voom and gvproxy.

### Scope and remaining Mac validation

Import, create, SSH, repeated boots, and stopped-VM cloning have passed on
MacOS 27.0.1. The source and clone ran at the same time with distinct Apple
identities and independent disk writes. The following still need testing on a
Mac; automated unit tests do not establish these results:

- **Reset:** create a disposable VM, write a file in its home directory, and
  record `voom --output json info <name>`. Reset it while running, start it,
  and run `guest verify`. The file should be gone and the Apple identity should
  change. The Voom ID, SSH port, and MAC should stay the same. Repeat reset,
  then stop and clone the VM. Check that the clone boots independently and
  neither operation changes the source image.
- **GUI:** close and reopen the window through the Dock and `voom gui`.
  The vfkit PID, Apple identity, and guest boot time should stay the same,
  with SSH working while hidden. Test stop while hidden, guest shutdown,
  Quit, refused or delayed shutdown, Force Quit, helper cleanup, and restart.
- **Shares and forwarding:** run the baked-image test above. Also check
  outbound DNS/HTTPS, manual TCP and UDP forwards, port conflicts, forwarding
  after restart, and isolation between VMs. Check that automatic forwarding
  excludes guest loopback listeners, handles offsets and occupied host ports,
  and removes a forward when its server stops.
- **Recovery and compatibility:** test interrupted commands, host reboot,
  removal and image-reference checks, CPU and memory changes, and an existing
  Linux vfkit VM. Import and create should still work after moving the source
  bundle aside. Test bundle portability on a second supported Apple Silicon
  host when available.

Keep the host and guest versions, Voom revision, helper versions, image build
metadata, logs, and `guest verify` JSON with the results. Release archive checks
and the native identity helper self-test run on MacOS CI.

MacOS guests have no serial console; use SSH, the window, or `voom logs <name>`
(vfkit logs by default). Egress/Agent Vault attachments, USB, PCI, NixOS
switching, disk growth, snapshots, and suspend/resume are unsupported.

For low-level identity experiments, `voom debug macos-clone-bundle SOURCE DEST`
prepares an unregistered copy and writes `identity-probe.json`; add
`--fresh-identity` to generate a new Apple identifier. Boot an unchanged-identity
control directly with vfkit, one copy at a time, with the source stopped.
`voom create` always assigns a fresh identifier, so it cannot test that control.

## Installation

> [!NOTE]
>
> Release and Nix installations include Voom's gvproxy build and, on MacOS,
> its vfkit build. Other host runtime dependencies, such as QEMU and SSH,
> are not included. See [Host Requirements](#host-requirements).

### Install a Release Build

Download a prebuilt archive for your platform from the [GitHub releases
page](https://github.com/mjrusso/voom/releases), verify it against
`checksums.txt`, and extract it into a directory on your `PATH`. Keep `voom`
and its bundled helpers together. Every platform includes `gvproxy`.
On MacOS, also keep `vfkit` and, on Apple Silicon, `voom-macos-identity` there.

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
> On Intel Macs, omit `./voom-macos-identity`. vfkit is ad-hoc signed with the
> virtualization entitlement; no Developer ID certificate is required.

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

- **Linux**: `/dev/kvm` access through [KVM](https://linux-kvm.org/),
  [QEMU](https://www.qemu.org/) (`qemu-system-<arch>`, `qemu-img`), and
  [OpenSSH](https://www.openssh.com/) (`ssh`).
- **MacOS**: Voom's bundled [vfkit](https://github.com/crc-org/vfkit) and
  [OpenSSH](https://www.openssh.com/) (`ssh`). Note that only Apple Silicon is
  supported.

Optional integrations: [virtiofsd](https://gitlab.com/virtio-fs/virtiofsd)
(Linux/QEMU shares), [Nix](https://nixos.org/download/) and
[`nixos-rebuild`](https://nixos.org/manual/nixos/stable/#sec-changing-config)
(for `voom nixos switch`), and [Git](https://git-scm.com/) (NixOS switch
metadata). Note that USB passthrough from Linux hosts requires a QEMU build
with XHCI and libusb `usb-host` support, and PCI passthrough requires a QEMU
build with VFIO `vfio-pci` support.

Install host runtime tools manually, or with your OS package manager.

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
`disk.raw`; existing Linux VMs using `disk.img` keep that filename until reset.
Both contain raw disks, and image import accepts either extension.

| Host OS | Host arch       | Image system    | Driver | Disk format |
|---------|-----------------|-----------------|--------|-------------|
| Linux   | x86_64          | `x86_64-linux`  | qemu   | qcow2       |
| Linux   | aarch64         | `aarch64-linux` | qemu   | qcow2       |
| Darwin  | arm64 / aarch64 | `aarch64-linux` | vfkit  | raw         |
| Darwin  | arm64 / aarch64 | `aarch64-darwin` | vfkit | raw bundle  |

## Commands Overview

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

### Aliases and shell completion

`shutdown` is an alias for `stop`, and `reboot` is an alias for `restart`.
`restart` and `reboot` stop and start the VM and its host helpers, including gvproxy.
Use `inspect` or `info` to inspect a VM, and `image inspect` or `image info`
to inspect an image. Every `list` command accepts `ls`. Every `remove` command
accepts `rm` and `delete`; `voom remove` also accepts `destroy`.
`gpu` is an alias for `pci`.

Generate completion scripts for Bash, Zsh, Fish, or PowerShell with
`voom completion <shell>`. Completion includes command names, existing VM and
image names, and image names for `--image`.

The Nix package installs Bash, Fish, and Zsh completion files in their standard
directories. Fish loads them automatically from Nix profiles. Bash and Zsh use
them when their completion systems are enabled.

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

## Explicit HTTP proxy attachments

An explicit HTTP proxy attachment connects a Linux VM to an external proxy or
broker. MacOS guests do not support these attachments. The broker can apply
VM-specific policy and supply credentials without storing them in the guest.
Each attachment records a private backend Unix socket on the host:

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
voom image inspect <name>
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
availability, LAN exposure, stale process records, and state consistency. On
MacOS, it probes the selected vfkit for GUI lifecycle and read-only sharing
extensions and reports its resolved path.

`voom logs <name>` defaults to the vfkit log for MacOS guests and the serial
log for Linux guests. Pass `--kind` to inspect helper logs (`qemu`, `vfkit`,
`gvproxy`, `auto-forward`, `share-mount`, `runtime-watch`).

For manual recovery, prefer command-level cleanup:

```sh
voom stop <name>
voom doctor
voom remove <name> --force
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

For MacOS image building and bundle imports, see [MacOS Guests](#macos-guests).
The cloud-init and sidecar workflow below applies to Linux.

Use `--install-guest-helpers` at import to install integrations on first boot.
Alternatively, bake them into a custom image and provide a *sidecar*: a small
JSON file declaring the image's capabilities.

The two approaches differ in setup and first-boot behavior:

|                           | `--install-guest-helpers`                                    | Bake into the image (+ sidecar)                  |
|---------------------------|--------------------------------------------------------------|--------------------------------------------------|
| **Works with**            | any `cloud-init`-capable image (e.g. stock Debian `generic`) | images you build yourself                        |
| **First-boot cost**       | one-time install on first boot                               | none: helpers are already present in the image   |
| **Network at first boot** | required (installs `jq`/`gawk`/`iproute2` from distro repo)  | not required                                     |
| **Enables**               | shares + auto port-forward                                   | shares + auto port-forward; `nixos switch` for NixOS images |
| **Setup**                 | one flag at image import                                     | a build pipeline                                 |

When passing `--install-guest-helpers`, Voom generates a `cloud-init` `NoCloud`
seed that writes Voom's helpers (`voom-portfwd`, the share-mount service, the
`/run/voom` control mount) and enables them on first boot. Note that the
install runs once, with subsequent starts reusing the helpers already on disk.
Alternatively, baking the helpers into the image avoids first-boot
latency and the package-install network dependency.

> [!TIP]
>
> The reference [nixos-config](https://github.com/mjrusso/nixos-config)
> image includes Nix and nix-direnv for per-project development environments.
> Its [VM module](https://github.com/mjrusso/nixos-config/blob/main/hosts/container/default.nix)
> installs Voom's control mount, forwarding and share helpers, and systemd
> services. The [build script](https://github.com/mjrusso/nixos-config/blob/main/scripts/bake-golden)
> produces the disk image and matching sidecar metadata.

See [Guest Image Contract](#guest-image-contract) for details on what a
full-featured image must provide, and [Images](#images) for the sidecar format
specification.

---

## Reference

Voom manages local development VMs through the host's VM stack. Voom does not
build images or run a persistent background control plane. Release and Nix
installations bundle gvproxy and, on MacOS, patched vfkit; Apple Silicon
packages also include the native identity helper. QEMU remains an external
dependency on Linux. Per-VM watchers manage automatic forwarding and MacOS
runtime cleanup while those VMs run.

Images must be explicitly imported, VMs must be created explicitly (referencing
an existing, already-imported image), and optional guest integrations are
always opt-in.

### Directories

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
the sibling helpers; overrides remain available for development. Run
`voom debug helpers --output json` to see the selected executable paths.
Startup checks the extensions required by the VM, and `voom doctor` probes
packaged-helper compatibility. Stock vfkit cannot replace the patched build
for MacOS guests or read-only directory shares. Other runtime overrides include
`VOOM_VIRTIOFSD` and `VOOM_QEMU_AARCH64_UEFI`.

Set the four directory overrides in the table to disposable directories to
keep tests separate from your normal VMs. See
[CONTRIBUTING.md](CONTRIBUTING.md) for examples.

### Images

**Names and IDs.** VM and image names are mutable labels (matching
`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`), decoupled from the stable generated IDs that
Voom uses for internal bookkeeping.

**Disks.** Each VM disk is an independent copy of the image disk. `voom image
remove` blocks while any VM references the image, unless `--force` is used; a
forced removal leaves existing VM disks and records in place. Those VMs cannot
start again without their image record; reset them to an available image before
restarting.

**Linux SSH access.** Set the required login user through the sidecar `user` field
or the `--ssh-user` flag (the flag wins if both are present). On start, Voom
writes a `NoCloud` seed authorizing the image's `sshIdentityPath` public key,
or, when unset, the readable defaults `~/.ssh/id_ed25519.pub`, `id_ecdsa.pub`,
`id_rsa.pub`, and `id_dsa.pub`. The seed grants passwordless sudo to root, and
to the login user when the two differ.

**Linux sidecar metadata.** A sidecar records an image's properties (`system`,
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
  "format": "qcow2",
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

This contract describes Linux images. For MacOS, use the
[bundle format and guest services](#macos-guests) described above.

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
helper writes `ports.json` through the reserved control share: `/run/voom` on
Linux or `/var/run/voom` on MacOS. Voom plans host forwards from fresh reports
and records installed/skipped rows in
`<runtime>/vms/<vm-id>/auto-forwards.json`. Rules:

- only TCP listeners are considered;
- guest port `22` is reserved for SSH;
- guest listeners must bind all guest interfaces (`0.0.0.0`, `::`, or `*`);
- runtime auto-forwards bind `127.0.0.1` on the host by default; pass
  `--bind <ip>` or `--lan` to expose them elsewhere;
- host port = guest port + configured auto-forward offset;
- changing the bind or offset removes runtime auto-forwards that no longer
  match immediately, without waiting for a guest report; forwards for the new
  bind or offset appear after the next valid report.

`voom forward discover <name>` is an audit/preview command; `voom forward list`
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
mounts declared shares from `/run/voom/mounts.json` on Linux or
`/var/run/voom/mounts.json` on MacOS, delivered over the reserved control share.
The image must advertise `controlShare` and `guestShareMount` and provide the
corresponding services. On both Linux and MacOS, `share add` checks these
capabilities before saving the declaration. Adding or removing a share requires
the VM to be stopped.

### USB Passthrough

USB passthrough provides a VM with direct access to USB devices connected to
the host. Voom supports USB passthrough from Linux hosts, but does **not**
support USB passthrough from MacOS hosts.

Use these commands to discover, assign, list, and remove USB devices:

```sh
voom usb discover
voom usb add dev board usb-0000:00:14.0@2-3.2
voom usb list dev
voom usb remove dev board
```

`voom list` reports the configured assignment count as `usb=N`. `voom info dev`
reports each assignment's host state (`connected`, `disconnected`, `inaccessible`,
or `unknown`) and runtime state (`active`, `missing`, `stopped`, or `unknown`).

The location consists of a stable host-controller identity, a USB protocol
domain, and the port path. On Linux, Voom finds the current bus number for the
controller whenever it starts or attaches the assignment, then passes that bus
and port to QEMU. Voom does not persist the numeric bus. QEMU checks the route
periodically, so a disconnected device can attach later and a device can
re-enumerate on the same route. QEMU exposes any USB device inserted on that
route to the guest while the VM is running. Multiple stopped VMs can use the
same assignment, but Voom prevents them from running with that assignment at
the same time.

If Voom cannot confirm a live USB removal, it keeps the VM running and retains
the assignment so the route remains reserved. Retry the removal to reconcile
the saved assignment with QEMU.

USB 2 and USB 3 sides of one physical connector are separate topology routes.
A device that reconnects at a different USB protocol can require a different
assignment. `voom usb discover` reports the route used by the currently
connected device.

The user running Voom needs read/write access to the corresponding
`/dev/bus/usb/<bus>/<address>` node. `voom usb discover` reports whether the
current device is accessible. `voom usb add` and `voom start` reject a connected
device if Voom cannot open its device node with read/write access. Configure
access through a host udev rule. On a systemd desktop, for example, this rule
grants the active user access to devices with vendor ID `1234`:

```udev
SUBSYSTEM=="usb", ATTR{idVendor}=="1234", TAG+="uaccess"
```

Replace `1234` with the vendor ID reported by `voom usb discover`. Use the
narrowest suitable vendor or product match for the hardware. Reload the rules
and reconnect the device after changing them. Voom does not install udev rules.

`uaccess` grants access only to users logged in at the host's own console. On a
headless host used over SSH, grant access through a group that the Voom user
belongs to instead:

```udev
SUBSYSTEM=="usb", ATTR{idVendor}=="1234", GROUP="plugdev", MODE="0660"
```

The guest must include the drivers and software required by the device. Guest
support can include XHCI, CDC ACM, USB serial, hidraw, or libusb. Guest udev
rules or group membership must also grant the intended account access.
You can add and remove assignments while the VM is running. Clones do not
inherit USB assignments.

### PCI Passthrough

PCI passthrough gives a VM exclusive use of a PCI Express device on the host,
such as a GPU for CUDA or ROCm workloads. Voom supports it for x86_64 QEMU VMs
on Linux hosts. While the VM runs, the host and other VMs cannot use the
device.

Use these commands to discover, assign, list, and remove PCI devices; `voom
gpu` is an alias for `voom pci`:

```sh
voom pci discover
voom pci add dev gpu 0000:41:00      # every function in the slot
voom pci add dev gpu 0000:41:00.0    # one function
voom pci list dev
voom pci remove dev gpu
```

A slot address assigns all of the slot's functions, such as a GPU's display,
audio, USB, and USB-C controller functions. A function address assigns only
that function; for compute workloads, the GPU function alone is enough. Voom
records each function's vendor:device ID, and `voom start` refuses the VM if
the hardware at that address has changed.

Adding or removing an assignment requires the VM to be stopped. A VM can have
up to 8 assignments. Each one is placed behind its own PCI Express root port in
the guest; guest PCI addresses can change when assignments are added or
removed. Multiple stopped VMs can use the same device, but only one of them can
run at a time. Clones do not inherit PCI assignments. `voom list` reports the
configured assignment count as `pci=N`, and `voom info dev` reports whether
each assignment is ready.

Voom does not change the host's configuration. Before assigning a device:

1. Enable the IOMMU (AMD-Vi or VT-d) in the firmware. On Intel hosts, also add
   `intel_iommu=on` to the kernel command line.
2. Bind every function you assign to the `vfio-pci` driver, typically at boot
   with the `vfio-pci.ids=<vendor>:<device>,...` kernel parameter. Every other
   device in the same IOMMU group must also be bound to `vfio-pci` or have no
   driver. If the device is the host's only GPU, the host loses its local
   display when `vfio-pci` claims it.
3. Grant the Voom user read/write access to `/dev/vfio/<group>`, for example
   with this udev rule for the `kvm` group:

   ```udev
   SUBSYSTEM=="vfio", GROUP="kvm", MODE="0660"
   ```

4. Raise the Voom user's soft and hard memlock limits (`ulimit -l`). VFIO locks
   all guest memory, and Voom requires a limit of at least the guest's memory
   plus 1 GiB, matching libvirt's x86 VFIO calculation.

`voom pci discover` reports whether each host slot is ready to assign, and why
not. `voom pci add`, `voom start`, and `voom doctor` report the same problems.
`voom start` also opens each device's IOMMU group to check that the kernel
reports it viable and that no other VM or process holds it.

The guest needs the device's drivers, such as the NVIDIA or ROCm driver.

### State and Runtime Layout

State is the persistent source of truth. JSON files are written atomically via
temp-file-and-rename; partial files are ignored on load.

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
and `AuxiliaryStorage`. Imported images preserve `bundle.json`, `restore.json`,
and `toolchain.json`. New VMs' active disks and any platform files live
under `<state>/vms/<vm-id>/storage-<generation>/`, selected by `vm.json`.
These files survive stop and restart.
Legacy Linux vfkit VMs may retain `disk.img`.

Runtime files are disposable. Notable paths under `<runtime>/vms/<vm-id>/`:

- `vm.process.json`, `gvproxy.process.json`, `auto-forward.process.json`,
  `runtime-watch.process.json`, `virtiofs-<tag>.process.json` — process IDs
  and system-specific start identities
- `seed.img` — Linux-only regenerated cloud-init NoCloud disk
  (`meta-data`, `user-data`, `network-config`)
- `control/metadata.json`, `control/mounts.json`, `control/ports.json` —
  host side of the reserved `voom-control` share
- `network.sock`, `qemu.mon`, `vfkit.sock`, `virtiofs-<tag>.sock` — driver and
  helper sockets

Cache holds disposable logs: `serial.log`, `qemu.log` or `vfkit.log`,
`gvproxy.log`, `auto-forward.log`, `share-mount.log`, `runtime-watch.log`, and
the global `events.jsonl` event stream. The stream retains one rotated generation at
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
dangling index entries, orphaned records, duplicate names or IDs, LAN exposure,
stale process records, stale auto-forward state, and malformed guest port reports.

The table below lists command effects. For MacOS, “disk” includes the Apple
platform files. `seed.img` is generated only for Linux.
Disk growth and `nixos switch` support Linux guests only.

| Command | Reads | Writes or removes |
| --- | --- | --- |
| `voom image import` | source disk, optional sidecar metadata | `<state>/state.json`, `<state>/images/<image-id>/image.json`, imported image disk |
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

## Development

For development setup, source builds, checks, generated docs, package
boundaries, and the release process, see [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Voom is released under the terms of the [MIT License](LICENSE).

Copyright (c) 2026, [Michael Russo](https://mjrusso.com).
