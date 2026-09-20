---
name: voom
description: "Create and control local Linux VMs with the Voom CLI on MacOS or Linux hosts. Use when the user mentions Voom, asks to operate a VM known to be managed by Voom, or explicitly asks to run work in a disposable local VM. Do not use merely because a task involves Linux, NixOS, testing, or isolation. Treat existing Voom VMs as user-owned state: inspect them freely, but do not disrupt or destroy them without explicit authorization."
---

# Voom

Use Voom on MacOS or Linux hosts to create and control local Linux virtual
machines. It uses vfkit on MacOS and QEMU/KVM on Linux. Voom has no daemon and
follows an explicit image import, VM create, and VM start lifecycle.

## Check the environment

Check for `/run/voom` first. If it exists, you are already inside a Voom guest.
Do not try to manage host VMs from that guest; explain the situation and stop.
Otherwise, confirm that the `voom` binary is available on the host.

Run `voom doctor` before creating or starting a VM, and when diagnosing host
problems. If a check fails, read its report; other Voom operations may still be
available.

## Use the installed CLI as the authority

The installed binary defines the command syntax for its version. Start with:

```bash
voom --help
voom image --help
voom forward --help
```

Use `voom <command> --help` before an unfamiliar operation. Do not guess flags
or run a mutating command without its required arguments merely to discover
its syntax.

Use `--output json` when inspecting state or consuming results
programmatically. In particular, do not parse Voom's human-readable tables:

```bash
voom list --output json
voom info <name> --output json
voom image list --output json
voom image inspect <name> --output json
voom forward list --output json
voom usb list <name> --output json
```

Human- and subprocess-oriented commands such as `ssh`, `console`, and `logs`
have command-specific payloads. The global output flag does not turn those
payloads into structured JSON. When only success or failure matters, use the
exit status.

## Select images by capability

Inspect the available images instead of inventing an image name or assuming a
host-specific default. Check that the image system matches the host
architecture and that its capabilities support the task. If exactly one image
is suitable, use it. Ask the user when the choice is ambiguous or no suitable
image exists.

Image capabilities gate guest integrations:

- Automatic forwarding requires `guestPortReport` and `controlShare`.
- Mounting declared shares requires `controlShare` and `guestShareMount`.
- An enabled explicit proxy attachment requires `controlShare`. A disabled
  attachment does not.
- `voom nixos switch` requires `nixosSwitch`.

If a capability is absent, report the gap. Retrying cannot add a missing image
capability.

## Treat start as asynchronous

`voom start` returns when the VM driver is reachable, before the guest has
necessarily booted. SSH, guest port reports, and automatic forwards may appear
later. Do not run a dependent SSH command immediately after `start`.

After starting, poll `voom ssh <name> -- true` every two seconds with a
two-minute deadline enforced by the agent's command runner. Retry connection
failures while the guest boots, but leave SSH errors visible so configuration
and authentication failures are not hidden. Do not rely on the external
`timeout` command: it is not installed by default on every MacOS host.

`voom events` provides best-effort VM, forwarding, and egress change
notifications, not guest or SSH readiness. Events are wake-up hints and must
be reconciled with current state.

An empty `voom forward list` immediately after start may mean "not yet."
Automatic forwards appear only after the guest reports listeners and the host
reconciles them.

## Workflows

### Use a disposable VM

Choose a distinctive `scratch-` name, record the VM name and ID returned by
`create`, and keep that identity for cleanup.

```bash
voom create <scratch-name> --image <image> --output json
voom start <scratch-name> --output json
voom ssh <scratch-name> -- <command>
voom stop <scratch-name> --output json
voom remove <scratch-name> --force --output json
```

Wait for SSH after `start`. Clean up only the scratch VM created for the task,
then report what was removed. If work fails before cleanup, preserve enough
identity information for the user to find the VM.

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

Verify the image capabilities, enable automatic forwarding if needed, and poll
the effective forwarding state:

```bash
voom forward auto enable <name> --output json
voom forward list <name> --output json
```

Read `voom forward auto enable --help` before selecting bind or LAN exposure.
Exposing a service beyond loopback requires explicit user intent.

Enabling automatic forwarding starts the watcher but does not wait for its
first reconciliation. Repeat `voom forward list <name> --output json` until the
expected installed row appears or the operation reaches its deadline.

### Configure an explicit proxy attachment

Use this workflow only when the user asks to connect a VM to an existing HTTP
CONNECT proxy available through a host Unix socket. The attachment does not
disable direct network access and is not an enforcement boundary. Voom does
not create or manage the proxy backend.

Inspect the VM first. Bind every change to its immutable ID, and assign a
different backend socket to each VM:

```bash
voom info <name> --output json
voom config egress set <name> --backend-socket <socket> --disabled --expect-id <vm-id> --output json
voom config egress enable <name> --expect-id <vm-id> --output json
voom info <name> --output json
```

When coordinating with an external attachment manager, save the declaration
with `--disabled`, complete the manager's policy checks, and then enable it.
Add `--ca-cert` only for a requested public CA certificate bundle. `set` and
`clear` require a stopped VM. `enable` and `disable` also work while the VM is
running.

`voom list --output json` reports the saved attachment state. `voom info
<name> --output json` also reports observed runtime state. Removing a VM does
not detach or remove its proxy backend.

### Rebuild a NixOS guest

Verify `nixosSwitch`, inspect the installed syntax, and then run the rebuild:

```bash
voom nixos switch --help
voom nixos switch <name> --flake <flake-ref>
```

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

Voom applies assignment changes immediately when the QEMU VM is running. If an
operation reports that the `/dev/bus/usb` node is inaccessible, report the
required host udev or ACL change. Do not weaken permissions for all USB devices.
The `usbStatus` rows from `info` separate host connectivity from QEMU runtime
state. Voom does not install guest flashing tools or host udev rules.

### Diagnose a VM

Start with state and host checks, then inspect logs or the console as needed:

```bash
voom doctor --output json
voom info <name> --output json
voom logs <name>
voom console <name>
```

## Safety and coordination

- Track which VMs were created during the current task. Existing VMs may hold
  active user work.
- Inspect existing VMs freely. Before `stop`, `remove`, `disk reset`, `rename`,
  resource changes, USB assignment changes, or egress configuration changes on
  one, require explicit authorization for that operation.
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
