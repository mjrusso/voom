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

`doctor` checks [host dependencies](installation.md#host-requirements), writable
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

Manual TCP/UDP forwarding supports Linux and MacOS guests. Shares and
automatic forwarding require Linux guest services; check them with
`voom guest verify dev`. Inspect forwarding state:

```sh
voom forward list dev
```

Read each row's status and reason. A saved mapping or reserved host port does
not prove that a forward is installed. Automatic forwarding also needs a recent
guest listener report and a running watcher. See
[port forwarding](linux-guests.md#port-forwarding), [shares](linux-guests.md#shares),
and [proxy attachment recovery](proxy-attachments.md#observation-and-recovery) for integration-specific
requirements and recovery.

### Forwarding reports uncertain state

If a manual or automatic port-forward request times out or loses its response,
Voom retries it with the same request ID. gvproxy applies each ID at most once,
so a retry is safe even if the first request has not arrived yet. After a
reported success, Voom checks the forwarding table.

If Voom still cannot confirm the change, it leaves the VM and gvproxy running,
keeps the recovery journal, reserves the affected ports, and blocks further
forwarding changes. Automatic forwarding retries through its watcher when
enabled. For other VMs, stop and start the VM if the change remains unresolved.

When you remove a manual forward, Voom saves the removal before asking gvproxy
to remove its listener. If it cannot confirm the change, the forward is no
longer in the saved configuration, but its listener may still be running. The port stays reserved
until recovery finishes, even if the CLI exits. Do not delete the recovery
journal to release ports.

A missing result may mean the request has not reached gvproxy yet. Retrying its
ID lets recovery continue in either case. If gvproxy has retired that ID, Voom
checks the forwarding table and uses a new ID if another change is needed.
If recovery cannot finish because the journal is from an older version or the
original gvproxy process cannot be verified, stop and start the VM to clear the
unresolved change.

During forwarding recovery, Voom stops helper processes only after confirming
that the VM process has exited. If its process record is missing, unreadable, or
invalid, Voom keeps the helper processes and port reservations. Run
`voom stop dev` and check for cleanup errors before restarting the VM.

After cleanup, a `vm stop` event with `reason=driver exited` confirms that Voom
observed the VM exit.

Private egress recovery and failed-startup cleanup can still stop the VM. Private
egress recovery stops the VM if the result of installing a gateway listener is
unknown, or if removing it fails, to prevent unintended guest egress. Before
that stop, a `vm stop-requested` event and `auto-forward.log` record the reason,
operation, endpoint, and error.

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
