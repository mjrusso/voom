---
name: voom
description: "Create and control local Linux and MacOS VMs with the Voom CLI. Use when the user mentions Voom, asks to operate a VM known to be managed by Voom, or explicitly asks to run work in a disposable local VM. Do not use merely because a task involves Linux, MacOS, NixOS, testing, or isolation. Treat existing Voom VMs as user-owned state: inspect them freely, but do not disrupt or destroy them without explicit authorization."
---

# Voom

Use Voom to manage local Linux and MacOS virtual machines. Linux guests run
with vfkit on MacOS hosts or QEMU/KVM on Linux hosts. MacOS guests require an
Apple Silicon Mac running MacOS 27 or later, a graphical login session, and
an installed ARM64 MacOS 27+ image bundle. Voom has no daemon: import an image,
create a VM, then start it.

## Check the environment

Confirm that you are on the host and that `voom` is available. Linux guests
with a control share have `/run/voom`. MacOS guests lack that marker;
its absence does not prove you are on the host. Use the session context and
confirm with the user if unclear. Do not manage host VMs from inside a guest.

Run `voom doctor` before creating or starting a VM and when diagnosing host
problems. Read failures; inspection and cleanup may still be available.

## Use the installed CLI as the authority

The installed binary defines its command syntax. Start with:

```bash
voom --help
voom image --help
voom forward --help
```

Use `voom <command> --help` before unfamiliar operations. Do not discover
syntax by guessing flags or running incomplete mutating commands.

Use `--output json` when inspecting state or consuming results
programmatically. In particular, do not parse Voom's human-readable tables:

```bash
voom list --output json
voom info <name> --output json
voom image list --output json
voom image inspect <name> --output json
voom forward list --output json
voom usb list <name> --output json
voom pci list <name> --output json
```

`ssh`, `console`, and `logs` have their own payloads; `--output json` does not
convert those to JSON. Use exit status when only success or failure matters.

## Select images by guest OS and capability

Choose the guest OS from the task, not the host OS. Inspect available images;
do not invent names or assume defaults. Match the guest OS to the task and
architecture to the host, then check capabilities. `arch: aarch64-darwin`
identifies a MacOS image or VM.
Use the sole suitable image; ask when the choice is ambiguous or none fits.

For Linux guests, image capabilities gate integrations:

- Automatic forwarding requires `guestPortReport` and `controlShare`.
- Mounting declared shares requires `controlShare` and `guestShareMount`.
- Configuring or enabling an explicit proxy attachment requires `controlShare`.
- `voom nixos switch` requires `nixosSwitch`.

If a capability is absent, report the gap. Retrying cannot add a missing image
capability.

## Treat start as asynchronous

`voom start` returns when the VM driver is reachable, before the guest has
necessarily booted. Wait for SSH before dependent work. Linux guest port
reports and automatic forwards may arrive later still.

After starting, poll `voom ssh <name> -- true` every two seconds with a
two-minute deadline enforced by the agent's command runner. Retry connection
failures while the guest boots, but leave SSH errors visible so configuration
and authentication failures are not hidden. Do not rely on the external
`timeout` command: it is not installed by default on every MacOS host.

After SSH is ready, use `voom guest verify <name> --output json` to check guest
health and supported integrations. It runs once without boot retries and
checks Nix when detected; MacOS guests do not require Nix.

`voom events` gives best-effort change notifications, not readiness. Reconcile
events with current state. On Linux guests, an empty forward list after start
can mean listener reports and reconciliation are still pending.

## Workflows

### Use a disposable VM

Choose a distinctive `scratch-` name. Record and report the VM name and stable
ID returned by `create`; keep that identity for cleanup.

```bash
voom create <scratch-name> --image <image> --output json
voom start <scratch-name> --output json
voom ssh <scratch-name> -- <command>
voom stop <scratch-name> --output json
voom remove <scratch-name> --force --output json
```

Wait for SSH after `start`. Clean up only the VM created for this task. Report
what was removed or left in place. Preserve its name and ID if work or cleanup
fails.

### Run a command in an existing VM

Inspect Voom state before acting on an existing VM:

```bash
voom info <name> --output json
voom ssh <name> -- <command>
```

`voom info` does not modify the VM. A remote command can modify or destroy guest
state, so run only commands authorized by the user's request. Do not stop or
reconfigure the VM afterward unless the user requested that operation.

### Reach a guest service from the host

Manual TCP/UDP forwarding supports Linux and MacOS without guest services.
Applications must listen on a guest network address or `0.0.0.0`; loopback-only
listeners need SSH tunnels.

Automatic forwarding requires Linux listener reports and discovers only TCP
listeners. Check image capabilities, enable it if needed, and poll forwarding
state:

```bash
voom forward auto enable <name> --output json
voom forward list <name> --output json
```

Read `voom forward auto enable --help` before selecting bind or LAN exposure.
Exposing a service beyond loopback requires explicit user intent.

Enabling starts the watcher without waiting for reconciliation. Poll the
forward list until the installed row appears or the deadline expires.

For manual mappings on either guest OS:

```bash
voom forward add <name> 5353 --protocol udp --host-port 15353 --bind 127.0.0.1 --output json
voom forward list <name> --output json
```

Manual forwards default to TCP. TCP and UDP can share a host address and port.
When removing a manual forward, match its protocol, bind address, and host port:

```bash
voom forward remove <name> 15353 --protocol udp --bind 127.0.0.1 --output json
```

### Handle a forwarding failure

After a forwarding error, inspect `voom info <name> --output json` and
`voom forward list <name> --output json` before retrying. Read `status` and
`reason`; a saved declaration or reserved port does not prove installation.

For an unknown mutation outcome, Voom tries to stop gvproxy. Confirmed exit
stops the VM and clears runtime reservations; saved forwarding configuration
remains. If exit cannot be confirmed, affected ports stay reserved, possibly
as `reservation` rows with `uncertain` status. Report these; do not treat them
as boot delay or delete runtime records to free ports.

Rejected forwards or failures before installation can leave the VM running.
Check observed state; restart an existing VM only when authorized.

### Configure an explicit proxy attachment

Use only for a requested Linux guest connection to an HTTP CONNECT proxy
through a host Unix socket. Direct network access stays enabled; this is not
an enforcement boundary. Voom does not create or manage the proxy backend.

Inspect the VM first. Bind every change to its immutable ID, and assign a
different backend socket to each VM:

```bash
voom info <name> --output json
voom config egress set <name> --backend-socket <socket> --disabled --expect-id <vm-id> --output json
voom config egress enable <name> --expect-id <vm-id> --output json
voom info <name> --output json
```

With an external attachment manager, save with `--disabled`, complete its
policy checks, then enable. Use `--ca-cert` only for a requested public CA
bundle. Target changes require a stopped VM; enabled-state changes work live.

`list` reports saved attachment state; `info` also reports runtime state.
Removing a VM does not detach or remove its proxy backend.

### Rebuild a NixOS guest

Verify `nixosSwitch`, inspect the installed syntax, and then run the rebuild:

```bash
voom nixos switch --help
voom nixos switch <name> --flake <flake-ref>
```

### Use a MacOS guest

Import a powered-off installed bundle with SSH enabled and your public key
already authorized. Voom does not install MacOS or inject keys:

```bash
voom image import <image> <bundle-dir> --ssh-identity <private-key> --output json
```

Supply `--ssh-user` if the bundle lacks a login account. Voom records the
private key's host path; keep it available. Do not use `--install-guest-helpers`.
Neither host nor guest needs Nix to run a prepared image.

`start` opens a GUI. Closing it hides the Dock entry and leaves the VM running.
Use `voom gui <name>` to reopen a running VM's window. Quit requests shutdown;
Force Quit terminates the VM. GUI login uses the guest password, which Voom
does not manage.

All image capabilities must be false. Shares, automatic forwarding, listener
reports, egress (even disabled), USB/PCI, `nixos switch`, and disk growth are
unsupported. Use SSH for file transfers.

Clone requires a stopped source and copies guest credentials and SSH host keys.
Create, clone, and disk reset assign fresh Apple identities; restarts preserve
identity. Reset stops the VM and replaces guest storage and platform files,
preserving manual forwards; clones omit them. CPU/memory changes require a
stopped VM and must meet recorded Apple minimums; creation defaults use baked
values.

### Attach a USB device on Linux

USB passthrough changes the VM configuration and gives the guest direct control
of any device connected to the assigned topology route. Inspect the host
devices and command syntax before making the assignment:

```bash
voom usb discover --output json
voom usb add <name> <device-name> <location> --output json
voom usb list <name> --output json
voom info <name> --output json
voom usb remove <name> <device-name> --output json
```

USB changes apply immediately to running QEMU VMs. For inaccessible
`/dev/bus/usb` nodes, report the required udev or ACL change; do not weaken
permissions for all USB devices. `info`'s `usbStatus` separates host
connectivity from runtime state. Voom installs no flashing tools or udev rules.

### Assign a PCI device on Linux

PCI passthrough gives an x86_64 QEMU VM exclusive use of a host PCI Express
device, such as a GPU. The VM must be stopped to change assignments. Inspect
the host slots before making the assignment:

```bash
voom pci discover --output json
voom pci add <name> <device-name> <slot-or-function> --output json
voom pci list <name> --output json
voom info <name> --output json
voom pci remove <name> <device-name> --output json
```

A slot like `0000:41:00` assigns all functions; `0000:41:00.0` assigns one.
Report host-driver conflicts, missing IOMMU groups, inaccessible `/dev/vfio`
nodes, or low memlock limits. Voom does not bind drivers, install udev rules,
or change limits.

### Diagnose a VM

Start with host checks, VM state, and logs:

```bash
voom doctor --output json
voom info <name> --output json
voom logs <name>
```

Verify the guest after SSH is ready. `voom console <name>` is a Linux serial
console. MacOS guests have no serial console; use `voom gui`, SSH, or vfkit
logs from `voom logs`.

## Safety and coordination

- Track which VMs were created during the current task. Existing VMs may hold
  active user work.
- Inspect existing VMs freely. Before `stop`, `remove`, `disk reset`, `rename`,
  resource changes, USB or PCI assignment changes, or egress configuration
  changes on one, require explicit authorization for that operation.
- A user request such as "stop my Voom VM named build" is authorization to
  stop that VM, but not to remove or reset it.
- Use `--force` only when removing a VM created during the current task or when
  the user explicitly requested non-interactive removal of the named VM. A
  prompt is not permission to broaden the operation.
- Do not remove images during ordinary scratch-VM cleanup. Images may be shared
  by other VMs.
- Prefer a `scratch-` name or a name supplied by the user for disposable VMs.
- Report the name and stable ID of each VM you create. When finished, report
  which scratch resources you removed and which you intentionally left in
  place.
