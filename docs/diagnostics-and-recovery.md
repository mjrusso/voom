# Diagnostics and recovery

[README](../README.md) · [Linux guests](linux-guests.md) ·
[MacOS guests](macos-guests.md) · [Command reference](commands/voom.md)

## Inspect the host and VM

Start with host checks and the VM's current state. Replace `dev` with your VM
name:

```sh
voom doctor
voom info dev
voom logs dev
```

`doctor` checks [host dependencies](../README.md#host-requirements), writable
directories, port availability, LAN exposure, stale process records, and state
consistency. It reports unsupported MacOS image capabilities and VM
declarations. These checks do not change VM state.

On MacOS, `doctor` probes the selected vfkit for GUI lifecycle and read-only
sharing extensions and reports its resolved path. MacOS guest startup requires
the GUI lifecycle extension; read-only sharing support is required only for
Linux guests configured with read-only shares. To inspect helper selection:

```sh
voom debug helpers --output json
```

Release and Nix installations include Voom's patched helpers. If an override
selects an incompatible executable, correct the override before retrying.
See [helper selection](reference.md#directories) for paths and overrides.

## Logs and consoles

`voom logs dev` defaults to the vfkit log for MacOS guests and the serial log
for Linux guests. Select a helper log with `--kind`:

```sh
voom logs dev --kind gvproxy
voom logs dev --kind runtime-watch
```

Available kinds are `serial`, `qemu`, `vfkit`, `gvproxy`, `auto-forward`,
`share-mount`, and `runtime-watch`. Which logs exist depends on the VM's driver
and integrations.

For Linux guests, `voom console dev` follows serial output; it is not an
interactive terminal, and the guest must be configured to write serial
output. For a running MacOS guest, `voom gui dev` shows its graphical window.
MacOS guests have no serial console.

## Guest verification

Wait for SSH and first-boot setup to finish before verifying a guest.
`start` returns when the driver is reachable, before the guest is necessarily
ready. The guest guides describe the checks for [Linux](linux-guests.md#guest-verification)
and [MacOS](macos-guests.md#guest-verification).

```sh
voom guest verify dev
voom guest verify dev --output json --timeout 30s
```

Verification runs once and exits nonzero if a check fails. It does not wait
for boot or start missing services. `--timeout` limits the whole run; the
default is two minutes. If the VM restarts or its configuration changes during
the checks, verification fails with `runtime-consistency`; run it again
against the current VM.

JSON output includes each check's output and errors, the driver process
fingerprint, and the storage generation when present. Missing optional
components appear as `skipped` in text and `"skipped": true` in JSON.

A successful check does not prove that restart, clone, reset, the GUI, or
forwarded traffic works; test those operations separately. SSH host-key
fingerprints are collected for inspection, not authenticated against a trusted
baseline; see [SSH behavior](reference.md#networking-and-ssh).

## Common failures

### SSH is unavailable after start

Check `voom info dev` and the driver and gvproxy logs. If the driver is still
running, allow time for boot and retry SSH. A running driver does not prove
that guest networking or SSH is ready.

For Linux cloud images, confirm that cloud-init is supported and that the
imported SSH user and public key are correct. First-boot helper installation
also needs guest network access. For MacOS, SSH and the authorized key must
already be configured in the imported bundle; Voom does not inject keys or
provision accounts. See the [Linux quickstart](linux-guests.md#getting-started)
or [MacOS import instructions](macos-guests.md#import-and-run).

### Shares or application forwards are missing

These integrations support Linux guests only. Check the image capabilities
and guest services with `voom guest verify dev`. Inspect forwarding state:

```sh
voom forward list dev
```

Read each row's status and reason. A saved declaration or reserved host port
does not prove that a forward is installed. Automatic forwarding also needs
a recent guest listener report and a running watcher. See
[port forwarding](linux-guests.md#port-forwarding), [shares](linux-guests.md#shares),
and [proxy attachment recovery](proxy-attachments.md#observation-and-recovery) for integration-specific
requirements and recovery.

### Reset reports an error

A cleanup error can occur after replacement storage has already been
committed; read the error before retrying reset, which discards guest changes.
Failure before the VM record is saved leaves the old storage selected, though
the VM may already be stopped. If the save's outcome is uncertain, Voom keeps
both storage copies. Do not delete either copy to resolve the error. See
[storage replacement](reference.md#storage-replacement) for commit and cleanup
behavior and [MacOS reset](macos-guests.md#reset-a-macos-guest) for platform
files and storage locks.

## Runtime cleanup

Prefer command-level cleanup when recovering a VM that should be stopped:

```sh
voom stop dev
voom doctor
```

Lifecycle cleanup applies to all VMs, including those without proxy
attachments. Voom records each helper's PID and system-specific start identity
at launch. Cleanup sends signals only when the current values match that
launch record. Start cleans up surviving helpers before launching replacement
runtime. Stop, remove, and disk reset retain process records and return an
error when identity or termination cannot be verified. Inspect the reported
PID and logs before removing retained recovery records.

On MacOS, stop, reset, and removal also wait for Virtualization.framework to
release disk and auxiliary-storage locks. After a guest exits through the GUI
or Force Quit, the runtime watcher cleans up helpers only after confirming
vfkit exit; inspection failures are reported in the `runtime-watch` log.

After confirming that the VM and all its helpers have stopped, you can remove
leftover files in its `<runtime>/vms/<vm-id>` directory. Use `voom debug paths`
to locate runtime and state directories. Removing files under `<state>`
deletes persistent state; see the [state and runtime layout](reference.md#state-and-runtime-layout).

If you intend to delete the VM and its storage, use `voom remove dev`.
Removal asks for confirmation; `--force` skips it. Removal is not necessary
for ordinary stop/start recovery.
