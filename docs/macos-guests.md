# MacOS guests

[README](../README.md) · [Diagnostics and recovery](diagnostics-and-recovery.md) ·
[Command reference](commands/voom.md)

MacOS guests have separate host and guest requirements:

| Component | Requirement |
|-----------|-------------|
| Host | Apple Silicon Mac running MacOS 27 or later, with a logged-in graphical session |
| Guest | ARM64 MacOS 27 or later, supplied as an installed image bundle |

Voom rejects older host versions and image bundles that declare an older
MacOS version. `voom guest verify` also checks the running guest's version
over SSH.

Voom includes its patched vfkit 0.6.4 build in MacOS release archives and Nix
packages; a separate vfkit installation is unnecessary.

[Install Voom](installation.md) before following the setup below.

- [Prepare an image](#prepare-an-image)
- [Import and run](#import-and-run)
- [Guest verification](#guest-verification)
- [Image bundle format](#image-bundle-format)
- [Identity and resources](#identity-and-resources)
- [Graphical sessions](#graphical-sessions)
- [Supported guest features](#supported-guest-features)
- [Manual port forwarding](#manual-port-forwarding)
- [Reset a MacOS guest](#reset-a-macos-guest)

## Prepare an image

Voom does not install MacOS or build images. It imports an installed guest in
the [bundle format](#image-bundle-format) below. The guest must have SSH
enabled and your public key authorized before import. Shut it down and keep
its disk and Apple platform files together.

For an example image builder, see the author's [nixos-config
builder](https://github.com/mjrusso/nixos-config#macos-vm-images). That
builder requires Nix and an Xcode toolchain with the MacOS 27 SDK. It installs
from an Apple restore image (IPSW) and includes Nix, nix-darwin, and Home
Manager. See that repository for setup instructions.

Voom does not provide an image builder. Running a prepared image requires
neither Nix nor Xcode on the host, and the guest does not need Nix. Other
builders must produce the same bundle format. Voom does not install guest
services in MacOS images.

## Import and run

With the source VM shut down, import its bundle using the SSH key authorized
in the guest. Replace `~/vms/macos-bundle` with your bundle directory:

```bash
voom image import macos ~/vms/macos-bundle \
  --ssh-identity ~/.ssh/id_ed25519
voom create mac-dev --image macos
voom start mac-dev
```

`--ssh-identity` takes the private key whose public key is already authorized
in the guest. Supply `--ssh-user <account>` if the bundle does not declare
the login account. Voom records the host path to the key. It does not copy the
key or change the guest's SSH access. MacOS SSH connections ignore host SSH
configuration.

CPU and memory default to the baked allocation. `start` opens a graphical
window; see [graphical sessions](#graphical-sessions) for closing and reopening
it and shutting down the guest.

`start` returns when vfkit's control socket is ready. The guest may still be
booting; wait for SSH before running `guest verify`. Voom uses gvproxy to
forward SSH to `192.168.127.3:22` in the guest. A standalone boot under
another VM tool does not validate Voom's networking; check SSH after starting
with Voom.

Once SSH is available, verify the guest and connect:

```sh
voom guest verify mac-dev
voom ssh mac-dev
```

To clone the guest, stop it first:

```sh
voom stop mac-dev
voom clone mac-dev mac-copy
```

## Guest verification

Run `voom guest verify mac-dev` after the guest has booted and SSH is available.
It checks the driver process, SSH access, MacOS version and architecture,
SSH host keys, boot information, and root access or passwordless sudo. It also
checks the Apple platform files and collects hardware identity.

Nix is optional. Verification checks installed Nix tools, the active system
link, and the daemon when detected. MacOS guests have no control-share,
listener-report, or automatic-forwarding checks.

Verification runs once; it does not wait for boot or start missing services.
It does not test the graphical window or lifecycle operations. See
[verification results](diagnostics-and-recovery.md#guest-verification) for
timeouts, JSON output, and handling failures.

## Image bundle format

The source bundle must contain nonempty regular files named `image.json`,
`restore.json`, `toolchain.json`, `disk.raw`, `HardwareModel`,
`MachineIdentifier`, and `AuxiliaryStorage`. Import copies these files while
holding disk and auxiliary-storage locks, then validates the copied files and
builds the image record from them. The source manifest is preserved as
`bundle.json`. After import, the source bundle can be moved or removed.

The source `image.json` must declare `schemaVersion: 1`, `system:
"aarch64-darwin"`, `format: "macos-bundle"`, and positive `cpuCount`,
`memoryMiB`, and `diskGiB` values. Set `user` to the login account, or supply
`--ssh-user` at import. `restore.json` records `version` (27 or later) and
`build`; `toolchain.json` records the tools used to build the image. The
bundle directory and its required files cannot be symlinks.

All five capabilities must be false: `metadataDisk`, `controlShare`,
`guestPortReport`, `guestShareMount`, and `nixosSwitch`. Import, create, boot,
reset, and guest verification reject images claiming these unsupported
capabilities. Starting an already-running VM validates its declarations and
returns without loading its imported image. Re-bake bundles that advertise
guest services; Voom does not convert them.

A source `image.json` example:

```json
{
  "cpuCount": 4,
  "memoryMiB": 8192,
  "diskGiB": 100,
  "schemaVersion": 1,
  "system": "aarch64-darwin",
  "format": "macos-bundle",
  "capabilities": {
    "metadataDisk": false,
    "controlShare": false,
    "guestPortReport": false,
    "guestShareMount": false,
    "nixosSwitch": false
  },
  "user": "guest"
}
```

The `--install-guest-helpers` import flag installs Linux helpers and is
rejected for MacOS bundles.

## Identity and resources

Create, clone, and reset copy the installed guest state and assign a fresh
Apple machine identifier. Clone requires a stopped source VM and uses its
current storage and platform metadata, even if its imported image has been
removed. Restarts retain the identifier. Clones also receive their own Voom
IDs, SSH ports, and NIC MAC addresses. Guest hostnames, SSH host keys, users,
and credentials remain those in the copied disk.

Voom checks the stored Apple machine identifier. Guest tools also expose a
platform UUID and serial number for comparison across boots.
`provisioning_UDID` can change between boots.

CPU and memory default to the baked allocation. Each bundle must record
Apple's `minimumCPUCount` and `minimumMemorySize` (bytes) in `restore.json`.
Both must be positive and no larger than the baked allocation. Voom allows
smaller allocations down to those minimums, rounding memory up to whole MiB.
Disk capacity comes from the image.

`info` and `image inspect` report platform paths, Apple identity, and the
source MacOS version/build. `doctor` checks storage and identity consistency.
The recorded OS version/build describe the image and do not track subsequent
guest OS upgrades.

## Graphical sessions

`voom start` opens a window titled with the VM name. The Dock and menu bar
show the app name `Voom: <vm name>`. Closing the window keeps the VM running
and removes the app from the Dock and Command-Tab. Run `voom gui mac-dev` to
restore the Dock entry and show the same display again. `gui` requires a
running MacOS VM. Minimizing the window or switching to another app keeps its
Dock entry available. All VM apps use the Voom icon.

Quit (Command-Q or the Dock menu) requests guest shutdown and keeps the app
responsive while shutdown completes. If the guest refuses shutdown or remains
running after 60 seconds, vfkit shows a message and leaves the VM running.
Force Quit terminates the VM immediately. A Voom background watcher cleans up
network helpers after confirmed vfkit exit, including after Force Quit or a
crash. If process inspection fails, the watcher retains the helpers and
reports the error in the `runtime-watch` log.

At each start, Voom creates an app bundle in the VM's runtime directory using a
copy of the selected vfkit executable. _(Voom signs the bundle automatically
with MacOS's built-in `codesign`, preserving vfkit's entitlements. It uses an
ad-hoc signature, which does not require an Apple developer account, signing
certificate, or Xcode installation.)_ App and window names are set at startup;
restart the VM after renaming it to update them.

MacOS guests require Voom's patched vfkit. An incompatible `VOOM_VFKIT`
override is rejected before starting helpers. Restart a VM to use an updated
vfkit build.

GUI login uses the guest account's password, which Voom does not manage.

## Supported guest features

MacOS guests support import, create, start, stop, restart, remove, clone,
reset, the graphical console, ordinary guest networking, SSH, remote command
execution, manual TCP/UDP port forwarding, persistent storage, and Apple
hardware identity management.

Voom-managed user shares, the reserved `voom-control` share, automatic port
forwarding, guest listener reports, and egress/proxy attachments are
unsupported. Use SSH to transfer files and execute commands. MacOS guests
also do not support USB or PCI passthrough, `nixos switch`, disk growth,
snapshots, or suspend/resume. There is no serial console; use the window, SSH,
or `voom logs <name>` for vfkit logs.

Voom does not attach virtiofs shares, publish control-share configuration,
start an automatic-forwarding watcher, or wait for guest-service reports for
MacOS guests. See [guest verification](#guest-verification) for the checks
that apply to MacOS.

Start, reset, and clone reject persisted shares, automatic forwarding, and
egress attachments before changing runtime or storage. You can still inspect,
stop, or remove the VM and clear unsupported settings. `voom doctor` warns
about unsupported image capabilities and VM settings without blocking
inspection or removal.

## Manual port forwarding

Manual TCP/UDP forwarding does not require guest services:

```sh
voom forward add mac-dev 8000
voom forward add mac-dev 5353 --protocol udp --host-port 15353
voom forward list mac-dev
```

The first command maps host `127.0.0.1:8000` to guest `192.168.127.3:8000`.
Applications must listen on the guest's network address or `0.0.0.0`. Use an
SSH tunnel for services that listen only on guest loopback.

Forwards can be added or removed while the VM is running or stopped. They
survive restart and reset. Clones do not inherit them. The source VM reserves
its host ports even when stopped, so choose a different host port or `--auto`
when recreating a forward on a clone.

Use the host port and protocol when removing a forward:

```sh
voom forward remove mac-dev 15353 --protocol udp
voom forward remove mac-dev 8000
```

## Reset a MacOS guest

```bash
voom disk reset mac-dev --image macos
voom start mac-dev
# Once the guest has booted and SSH is available:
voom guest verify mac-dev
```

Reset stops the VM and replaces its disk, hardware model, auxiliary storage,
and Apple machine identifier from the selected image. It discards guest
changes and generates a fresh Apple identity. The VM name, Voom ID, CPU and
memory allocation, SSH port, NIC MAC, and manual forwards stay the same. Voom
adopts the selected image's SSH access metadata. Unsupported declarations and
images that cannot support the VM's resource allocation are rejected before
shutdown.

Reset can repair missing boot files, provided the source image is complete and
the old runtime has released its storage locks.

See [storage replacement](reference.md#storage-replacement) for commit and
failure handling shared by both guest OSes.

Stop, reset, and removal keep the VM's records when Voom cannot confirm that
the runtime has exited. On MacOS, these commands also wait for
Virtualization.framework to release the disk and auxiliary-storage locks.
