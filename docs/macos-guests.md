# MacOS guests

[README](../README.md) · [Command reference](commands/voom.md)

MacOS guests require an Apple Silicon Mac running MacOS 27 or later and a
logged-in graphical session. Voom includes its patched vfkit 0.6.4 build in
MacOS release archives and Nix packages; a separate vfkit installation is
unnecessary.

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

Import the completed, stopped bundle using the SSH key authorized in the
guest. Replace `~/vms/macos-bundle` with your bundle directory:

```bash
voom image import macos ~/vms/macos-bundle \
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
forward SSH to `192.168.127.3:22` in the guest. A standalone boot under
another VM tool does not validate Voom's networking; check SSH after starting
with Voom.

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

`voom start` opens a window titled with the VM name. Closing the window hides
it and keeps the VM running. Click its Dock icon or run `voom gui mac-dev` to
show the same display again. `gui` requires a running MacOS VM.

Quit (Command-Q or the Dock menu) requests guest shutdown and keeps the app
responsive while shutdown completes. If the guest refuses shutdown or remains
running after 60 seconds, vfkit shows a message and leaves the VM running.
Force Quit terminates the VM immediately. A Voom background watcher cleans up
network helpers after confirmed vfkit exit, including after Force Quit or a
crash. If process inspection fails, the watcher retains the helpers and
reports the error in the `runtime-watch` log.

The process name and Dock icon belong to vfkit; the window title uses the VM
name at startup. Restart a VM to use an updated vfkit build. MacOS guests
require Voom's patched vfkit. An incompatible `VOOM_VFKIT` override is
rejected before starting helpers.

GUI login uses the guest account's password, which Voom does not manage. If
the image builder generated a password you do not know, connect with `voom
ssh` and run `sudo /usr/bin/passwd "$(id -un)"` inside the guest to set one.

## Supported guest features

MacOS guests support import, create, start, stop, restart, remove, clone,
reset, the graphical console, ordinary guest networking, SSH, remote command
execution, persistent storage, and Apple hardware identity management.

Voom-managed user shares, the reserved `voom-control` share, application port
forwards (manual and automatic), guest listener reports, and egress/proxy
attachments are unsupported. The host-side SSH mapping remains available. Use
SSH to transfer files and execute commands. MacOS guests also do not support
USB or PCI passthrough, `nixos switch`, disk growth, snapshots, or
suspend/resume. There is no serial console; use the window, SSH, or `voom logs
<name>` for vfkit logs.

Voom does not attach virtiofs shares, publish control-share configuration,
start an automatic-forwarding watcher, or wait for guest-service reports for
MacOS guests. `guest verify` checks SSH, architecture, platform storage,
hardware identity, and other supported probes, including Nix when detected.

Start, reset, and clone reject persisted shares, application forwards,
automatic forwarding, and egress attachments before changing runtime or
storage. Inspection, stop, remove, share/forward removal, automatic-forwarding
disablement, and egress clearing remain available for cleanup. `voom doctor`
warns about unsupported image capabilities and VM declarations without
blocking inspection or removal. Linux guests retain sharing, forwarding, guest
services, and egress support, including through vfkit on MacOS hosts.

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
memory allocation, SSH port, and NIC MAC stay the same. Voom adopts the
selected image's SSH access metadata. Unsupported declarations and images that
cannot support the VM's resource allocation are rejected before shutdown.
Reset cannot change a VM between Linux and MacOS.

Reset can repair missing boot files, provided the source image is complete and
the old runtime has released its storage locks.

See [storage replacement](reference.md#storage-replacement) for commit and
failure handling shared by both guest OSes.

Stop, reset, and removal keep the VM's records when Voom cannot confirm that
the runtime has exited. On MacOS, these commands also wait for
Virtualization.framework to release the disk and auxiliary-storage locks.
