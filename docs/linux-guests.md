# Linux guests

[README](../README.md) · [Command reference](commands/voom.md)

Linux guests run on Linux through QEMU/KVM and on Apple Silicon MacOS through
vfkit. See the [support tables](../README.md#supported-hosts-and-guests) for
host requirements and feature availability.

## Custom images

Use `--install-guest-helpers` at import to install integrations on first boot.
Alternatively, bake them into a custom image and provide a *sidecar*: a small
JSON file declaring the image's capabilities. Pass it explicitly at import:

```sh
voom image import nixos ./disk.qcow2 --meta ./metadata.json
```

The two approaches differ in setup and first-boot behavior:

|                           | `--install-guest-helpers`                                    | Bake into the image (+ sidecar)                  |
|---------------------------|--------------------------------------------------------------|--------------------------------------------------|
| **Works with**            | systemd image with cloud-init and virtiofs (e.g. Debian `generic`) | images you build yourself                        |
| **First-boot cost**       | one-time install on first boot                               | none: helpers are already present in the image   |
| **Network at first boot** | required (installs `jq`/`gawk`/`iproute2` from distro repo)  | not required                                     |
| **Enables**               | shares, listener reports, automatic forwarding, proxy attachments | integrations declared by the sidecar; `nixos switch` for NixOS images |
| **Setup**                 | one flag at image import                                     | a build pipeline                                 |

When passing `--install-guest-helpers`, Voom generates a `cloud-init`
`NoCloud` seed that writes Voom's helpers (`voom-portfwd`, the share-mount
service, the `/run/voom` control mount) and enables them on first boot. Note
that the install runs once, with subsequent starts reusing the helpers already
on disk. Alternatively, baking the helpers into the image avoids first-boot
latency and the package-install network dependency.

> [!TIP]
>
> The reference [nixos-config](https://github.com/mjrusso/nixos-config)
> image includes Nix and nix-direnv for per-project development environments.
> Its [VM module](https://github.com/mjrusso/nixos-config/blob/main/hosts/container/default.nix)
> installs Voom's control mount, forwarding and share helpers, and systemd
> services. The [build script](https://github.com/mjrusso/nixos-config/blob/main/scripts/bake-golden)
> produces the disk image and matching sidecar metadata.

See the [guest image contract](#guest-image-contract) for requirements and the
[sidecar metadata](#image-metadata) below for capability declarations.

## Image metadata

**Linux SSH access.** Set the required login user through the sidecar `user`
field or the `--ssh-user` flag (the flag wins if both are present). On start,
Voom writes a `NoCloud` seed authorizing the image's `sshIdentityPath` public
key, or, when unset, the readable defaults `~/.ssh/id_ed25519.pub`,
`id_ecdsa.pub`, `id_rsa.pub`, and `id_dsa.pub`. The seed authorizes root as
well as the configured login user, and grants passwordless sudo to the login
user when it is not root.

**Linux sidecar metadata.** A sidecar records an image's properties (`system`,
`format`, login `user`, and so on) and declares which guest integrations are
available:

- `controlShare` — guest can mount the reserved `voom-control` share at
  `/run/voom`
- `guestPortReport` — guest runs `voom-portfwd` and writes
  `/run/voom/ports.json`
- `guestShareMount` — guest can mount declared host shares
- `nixosSwitch` — guest supports `voom nixos switch`

A sidecar for a NixOS image with all Linux integrations:

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
declaring at least `user`, `system`, `format`, `baked_at`, `flake_rev`, and
the capabilities the image actually ships. The author's NixOS config does this
with outputs named `.#images.<system>.<format>`; see
[scripts/bake-golden](https://github.com/mjrusso/nixos-config/blob/main/scripts/bake-golden)
for a reference implementation.

## Guest image contract

A Linux image must boot on the host's architecture, use `qcow2` for QEMU or
`raw` for vfkit, and obtain networking through gvproxy's DHCP service. SSH
requires sshd on TCP 22 and authorization for the host's SSH public key. A
cloud-init image can use Voom's NoCloud seed to configure the login user and
public keys; a baked image can provide that access itself. Serial console
output requires guest serial configuration. Graceful shutdown requires a
working ACPI/vfkit shutdown path.

Optional features require the capabilities and guest services below:

| Feature | Required capabilities | Guest requirements |
| --- | --- | --- |
| Host directory shares | `controlShare`, `guestShareMount` | virtiofs, mount tools, and a service that reads `/run/voom/mounts.json` and mounts the declared shares |
| Listener reports and automatic forwarding | `controlShare`, `guestPortReport` | `voom-portfwd` or a helper implementing the report contract below |
| Explicit proxy attachments | `controlShare` | mount `voom-control` at `/run/voom`; applications must opt into the proxy |
| `nixos switch` | `nixosSwitch` | NixOS with Nix, its daemon, and root access or passwordless sudo for the target user |

`controlShare` means that the guest mounts the reserved `voom-control`
virtiofs share at `/run/voom`. Capabilities declare services that the image
provides; setting a flag does not install them. Manual TCP/UDP forwards do not
require a listener-reporting helper.

If the guest has a firewall, allow inbound SSH and any services you intend to
forward. Voom manages host-side port forwards through gvproxy; it does not
configure or bypass the guest firewall.

### `voom-portfwd` contract

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

Field rules: `schemaVersion` must be integer `1`; `generatedAt` must be an RFC
3339 timestamp; `proto` must be `"tcp"` for v1; `pid` and `process` are
optional. Ports must be in 1–65535. `addr` must be an IP literal, `"*"`, or
empty; wildcard and empty addresses are eligible for automatic forwarding.

The bundled helper defaults to `/run/voom/ports.json` and a two-second
interval. Override these with `VOOM_PORTFWD_OUTPUT` and
`VOOM_PORTFWD_INTERVAL` (seconds). It writes reports only while the output
directory is mounted.

The bundled systemd unit assumes the reserved share is mounted by
`run-voom.mount`:

```ini
[Unit]
Description=Report guest TCP listeners to voom
Wants=network-online.target run-voom.mount
After=network-online.target run-voom.mount

[Service]
ExecStart=/usr/local/sbin/voom-portfwd
Restart=always
RestartSec=2s

[Install]
WantedBy=multi-user.target
```

For reference, see the author's NixOS config at
[`hosts/container/default.nix`](https://github.com/mjrusso/nixos-config/blob/main/hosts/container/default.nix)
(helper script, systemd unit, `voom-mount-shares` service, `/run/voom`
virtiofs control-share mount).

## Port forwarding

Automatic forwarding maps eligible TCP listeners on all guest interfaces to
host loopback by default. It skips guest SSH port 22 and unavailable host
ports. The following examples use the running Linux VM `deb` from the
[quickstart](../README.md#linux-guest) and require Python 3 in the guest:

```bash
voom forward auto enable deb
voom ssh deb -- 'nohup python3 -m http.server 8080 >/dev/null 2>&1 &'
voom forward list deb
```

Once `forward list` confirms that host port 8080 is installed:

```sh
curl http://127.0.0.1:8080
```

If you're running multiple VMs that each bind the same guest ports, you can
assign each VM an offset to prevent host collisions. For example, `voom
forward auto enable deb --offset 10000` maps host `18080` to guest `8080`
(`host port = guest port + offset`). To adjust an existing offset, run `voom
forward auto offset deb <n>`.

To expose auto-forwards beyond loopback, pass `--bind <ip>` or `--lan` when
enabling auto-forwarding. For example, `--bind 192.168.1.10` listens on that
local host address (if assigned to the host), while `--lan` listens on
`0.0.0.0`.

If you would prefer to explicitly expose ports:

```bash
voom ssh deb -- 'nohup python3 -m http.server 9090 >/dev/null 2>&1 &'
voom forward add deb 9090      # host 127.0.0.1:9090 -> guest 9090
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
forwarding discovers TCP listeners only. If a forwarding request has an
unknown outcome, Voom stops the VM before releasing its reservation. If
gvproxy termination cannot be confirmed, Voom retains the reservation and
returns an error. Unconfirmed new automatic forwards appear in `voom forward
list` as `reservation` rows with `uncertain` status.

## Networking details

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
Linux. Voom plans host forwards from fresh reports and records
installed/skipped rows in `<runtime>/vms/<vm-id>/auto-forwards.json`. Rules:

- only TCP listeners are considered;
- guest port `22` is reserved for SSH;
- guest listeners must bind all guest interfaces (`0.0.0.0`, `::`, `*`, or an
  empty address in the report);
- runtime auto-forwards bind `127.0.0.1` on the host by default; pass
  `--bind <ip>` or `--lan` to expose them elsewhere;
- host port = guest port + configured auto-forward offset;
- changing the bind or offset removes runtime auto-forwards that no longer
  match immediately, without waiting for a guest report; forwards for the new
  bind or offset appear after the next valid report.

`voom forward discover <name>` is an audit/preview command; `voom forward
list` shows effective rows including skipped auto-forwards with an
explanation.

## Shares

`voom share add <name> <tag> <host-path> <guest-path>` declares a `virtio-fs`
share in `vm.json`; append `--ro`/`--readonly` for a read-only mount. The tag
`voom-control` is reserved. On QEMU, Voom exports each share through a
`virtiofsd` helper; on vfkit, it attaches native virtio-fs devices. The guest
reads `/run/voom/mounts.json` over the reserved control share and mounts the
declared directories. MacOS guests do not support these shares.

The Linux image must advertise `controlShare` and `guestShareMount` and
provide the corresponding services. `share add` checks these capabilities
before saving the declaration. Adding or removing a share requires the VM to
be stopped.

## NixOS switching

An image with `nixosSwitch` supports applying a host flake to a running NixOS
Linux guest:

```sh
voom nixos switch dev --flake .#vm
```

Replace `.#vm` with the NixOS configuration in your flake. The host needs Nix
and `nixos-rebuild`; the guest needs root access or passwordless sudo for the
configured target user. Voom runs `nixos-rebuild switch --target-host` over
the VM's SSH mapping and records the successful switch. This command does not
apply nix-darwin configurations to MacOS guests.

## USB passthrough

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

`voom list` reports the configured assignment count as `usb=N`. `voom info
dev` reports each assignment's host state (`connected`, `disconnected`,
`inaccessible`, or `unknown`) and runtime state (`active`, `missing`,
`stopped`, or `unknown`).

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
current device is accessible. `voom usb add` and `voom start` reject a
connected device if Voom cannot open its device node with read/write access.
Configure access through a host udev rule. On a systemd desktop, for example,
this rule grants the active user access to devices with vendor ID `1234`:

```udev
SUBSYSTEM=="usb", ATTR{idVendor}=="1234", TAG+="uaccess"
```

Replace `1234` with the vendor ID reported by `voom usb discover`. Use the
narrowest suitable vendor or product match for the hardware. Reload the rules
and reconnect the device after changing them. Voom does not install udev
rules.

`uaccess` grants access only to users logged in at the host's own console. On
a headless host used over SSH, grant access through a group that the Voom user
belongs to instead:

```udev
SUBSYSTEM=="usb", ATTR{idVendor}=="1234", GROUP="plugdev", MODE="0660"
```

The guest must include the drivers and software required by the device. Guest
support can include XHCI, CDC ACM, USB serial, hidraw, or libusb. Guest udev
rules or group membership must also grant the intended account access. You can
add and remove assignments while the VM is running. Clones do not inherit USB
assignments.

## PCI passthrough

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
up to 8 assignments. Each one is placed behind its own PCI Express root port
in the guest; guest PCI addresses can change when assignments are added or
removed. Multiple stopped VMs can use the same device, but only one of them
can run at a time. Clones do not inherit PCI assignments. `voom list` reports
the configured assignment count as `pci=N`, and `voom info dev` reports
whether each assignment is ready.

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
