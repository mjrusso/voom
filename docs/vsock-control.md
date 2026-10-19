# Vsock guest control

Voom owns one control process per running VM. Guests initiate the connection;
there is no global control daemon and no SSH control transport. Virtiofs is used
for user shares. A vsock VM has no `voom-control` virtiofs device.

## Endpoints and lifecycle

On vfkit hosts, both Linux and Darwin guests connect to host CID 2, port 49152.
Voom listens on `<runtime>/vms/<id>/control.sock`, mode 0600 in the private VM
runtime directory. vfkit receives:

```text
--device virtio-vsock,port=49152,socketURL=/absolute/runtime/vms/id/control.sock,listen
```

On Linux/QEMU hosts, each VM has its own kernel-assigned host vsock port and a
random guest CID. Voom opens `/dev/vhost-vsock`, reserves the CID with
`VHOST_VSOCK_SET_GUEST_CID`, and passes that same descriptor to QEMU at fd 3.
QEMU receives `vhost-vsock-pci,guest-cid=<cid>,vhostfd=3`. This prevents a gap
between allocation and driver launch; collisions with other Voom instances or
other hypervisors are rejected by the kernel. The descriptor closes on failed
start or driver exit, releasing the CID. See the [kernel implementation](https://linux.googlesource.com/linux/kernel/git/torvalds/linux/+/81122fba08fa3ccafab6ed272a5c6f2203923a7e/drivers/vhost/vsock.c).

The host accepts only the reserved peer CID. QEMU publishes the selected host
port as `opt/voom/control-port` through `-fw_cfg`. The Linux adapter reads
`/sys/firmware/qemu_fw_cfg/by_name/opt/voom/control-port/raw`; on vfkit that entry
is absent and the adapter uses 49152. Linux guests need `virtio_vsock`, its virtio
transport, `qemu_fw_cfg` on QEMU, and `virtiofs` when sharing files. Linux hosts
need usable `/dev/kvm`, `/dev/vhost-vsock`, and AF_VSOCK support.

The control listener starts before the driver. Its endpoint is recorded in
`control-endpoint.json`; it waits at most a minute for driver startup. The
existing per-VM runtime watcher owns cleanup after unexpected driver exit.
Stop and failed start stop the control service and remove its socket, endpoint,
and reports. The kernel releases the CID when QEMU releases its descriptor.
`voom logs <vm> --kind control` and `voom doctor` expose failures.

## Version 1 protocol

Streams carry UTF-8 JSON objects separated by newline. A frame, including its
newline, is at most **8 MiB**. This includes the existing maximum 4 MiB public CA
bundle, encoded as a JSON string. Both readers and writers enforce the limit.
Every envelope contains integer `version: 1`, string `type`, and optional
`payload`. Unsupported versions, wrong message order, malformed JSON, truncated
frames, invalid listener reports, and read/write timeouts close the connection
and are logged. Newline characters inside strings must be escaped by JSON.

The conversation is synchronous:

1. Guest sends `hello` with null payload.
2. Host sends `config` with `metadata`, `mounts`, and optional `egress` and
   `egressCA`. Mount entries contain only `tag`, `guestPath`, and `readonly`;
   host filesystem paths are never sent.
3. Guest applies configuration and sends `report`, containing `ports` (the
   existing schema-1 listener report) and an `errors` array for mount failures.
4. Host sends the current full configuration again. The guest reports every
   two seconds. This repeats until disconnection.

There are no deltas to recover or configuration acknowledgements to correlate:
each report applies to the immediately preceding configuration. Every new
connection starts again at `hello` and receives a complete replay, including
current egress enable/disable state. Guest reconnect delay is two seconds;
connect timeout is ten seconds and stream I/O timeout is twenty seconds.

Host receipt time controls report freshness, avoiding host/guest clock skew.
Disconnection or protocol failure deletes the report; the forward reconciler
removes automatic forwards when a vsock report is missing or stale. A killed
control process leaves at most the existing 30-second freshness window.
Manually declared forwards and SSH forwarding follow their existing lifecycle.

The guest stores configuration in local `/run/voom` (Linux) or `/var/run/voom`
(Darwin). Existing egress consumers keep their file interface. Egress CA paths
in manifests match the guest platform. Unchanged files are not rewritten.
The NixOS service reports readiness after its first configuration and listener
report; egress trust setup waits for this service. Darwin uses nix-darwin's
`command` option so launchd waits for `/nix/store` before executing Python.
Mount/listener discovery and OS service wiring remain platform adapters.

Darwin's native sockaddr_vm has a length byte and a family byte. The shared
module uses the Darwin layout explicitly; Linux uses Python's AF_VSOCK API.
Darwin mount listings expose generic `virtio-fs` and `AppleVirtIOFS`, so the
adapter checks the target, filesystem, and read-only option; the listing cannot
prove a mount tag. Guest verification recognizes this output too.

## Image compatibility

Stop all running VMs with the current Voom binary before installing this
experimental build or changing guest agents and image capabilities. The host
endpoint and vsock device are created at VM start. Transport migration requires
a cold stop/start with a matching agent and image contract; an agent restart or
guest reboot does not add the device to an existing VM process.

`vsockControl: true` explicitly declares **version 1** support. It is independent
of `controlShare`; Voom never treats the latter as vsock support. If both are
true, vsock takes precedence. Shares still require `guestShareMount`; automatic
forwarding still requires `guestPortReport`.

New `--install-guest-helpers` imports install the Python vsock agent and declare
`vsockControl: true`, `controlShare: false`. Existing imported Linux images
retain the transport stored in their image record. Their reserved virtiofs
transport and legacy cloud-init helpers remain available because deployed baked
images and existing helper-image records depend on them. Re-import the source
with the updated helpers to opt into vsock; changing a capability bit alone does
not install an agent.

Keep legacy guests on their existing agent generation until migration to a
vsock-enabled image. Installing the new agent alone does not add the required
host vsock device to a legacy VM; its image contract and cold-start device setup
must change together.

MacOS bundles with no guest integrations remain usable. Starting a MacOS image
that declares only legacy `controlShare` guest services fails with an upgrade
message. Reset rejects that contract before stopping the current VM. Install the
revised agent in a disposable clone first, or bake a new image. No Full Disk Access entitlement or SSH coordination fallback is required.
Applications reading user-shared files still follow MacOS privacy controls.

## Validation and repository ordering

The development baseline was Voom `f46eda5`, nixos-config `bea35d0`, and
nixos-config-private `39fe357`. All three working trees and stash lists were
clean. nixos-config still pins Voom `48daad9`; it must use the new Voom revision
when these changes are published. Until then, use
`--override-input voom path:/absolute/path/to/voom` when evaluating shared
configuration. Do not build or accept an old installed Voom binary.

On Linux, the old binary booted a disposable Debian 12 cloud image through
QEMU/KVM; SSH, cloud-init completion, the reserved mount, and port reports were
verified before changing the runtime transport. The new binary then passed
configuration delivery, read/write and read-only mounts, listener forwarding
and closure, agent reconnect, cold restart, proxy traffic, live egress replay,
and concurrent VM isolation. Runtime process records, sockets, and endpoint
records were removed after stop/remove. These results do not verify MacOS.

A reproducible test uses its own temporary keys, VOOM directories, imported
images, and VMs. It leaves command evidence and `result.json` under the printed
`/tmp/voom-vsock-*` directory. The input cloud image is read only. A public CA
bundle larger than 1 MiB exercises configuration framing. Run from Voom:

```sh
nix build .#voom --out-link /tmp/voom-vsock-package
nix shell nixpkgs#qemu nixpkgs#virtiofsd nixpkgs#openssl -c \
  python3 scripts/verify-linux-vsock.py \
    --voom /tmp/voom-vsock-package/bin/voom \
    --disk /absolute/path/to/debian-12-genericcloud-amd64.qcow2
```

The nonroot write check uses an unprivileged guest process with the host user's
numeric UID/GID. Voom's existing rootless virtiofsd export requires matching IDs
for file creation. On this host, Debian's default `1000:1000` produced `EPERM`,
while `1000:100` matched the host and succeeded. The exporter arguments and
ownership semantics are unchanged; see [virtiofsd's rootless documentation](https://gitlab.com/virtio-fs/virtiofsd/-/blob/main/README.md#running-as-non-privileged-user).

The script also checks that a live CID cannot be claimed, a stopped CID can be
claimed again, and killing the disposable QEMU process causes helper cleanup.
The current fixture targets x86_64 Linux. Linux-on-vfkit and Darwin-on-vfkit
runtime validation require a Mac.

## Mac clone test, bake, and acceptance

On an Apple Silicon Mac running MacOS 27 or later with a graphical session, from the Voom checkout:

```sh
nix build .#voom --out-link /tmp/voom-vsock-package
nix run ../nixos-config#verify-macos-voom \
  --override-input voom "path:$PWD" -- \
  --voom /tmp/voom-vsock-package/bin/voom \
  --bundle "$HOME/vms/golden-aarch64-darwin" \
  --identity "$HOME/.ssh/id_ed25519" \
  --agent-source "$PWD/../nixos-config/packages/voom-macos-guest/agent.py" \
  --timeout 180
```

`--agent-source` creates a disposable APFS clone of the bundle, updates only its
capabilities, and imports it into isolated Voom state. After SSH becomes ready,
it stops the old job and installs the adapter plus the selected Voom binary's
shared module in `/var/lib/voom-test-agent`. It replaces the clone's launchd
plist symlink with a test plist containing the `/nix/store` startup guard.
SSH is used only for installation and diagnostics. Coordination uses vsock.
The test verifies actual read/write and read-only shares, automatic forwarding
and listener closure, agent restart, and cold restart. It waits for host-received vsock reports before SSH diagnostics
on baked-image boot and cold restart. It fingerprints the original bundle before and after, removes disposable objects and the test
bundle, and retains evidence. A timeout is a failure, not a successful probe.

After that test passes, publish the Voom revision, update nixos-config's Voom
pin to it, publish shared configuration, and update the private configuration's
nixos-config pin. Inspect each lockfile change. Bake a new candidate without
overwriting the existing golden bundle:

```sh
nix run ../nixos-config#bake-macos -- \
  --flake ../nixos-config-private \
  --identity "$HOME/.ssh/id_ed25519" \
  --output "$HOME/vms/golden-aarch64-darwin-vsock-candidate"

nix run ../nixos-config#verify-macos-voom -- \
  --voom /tmp/voom-vsock-package/bin/voom \
  --bundle "$HOME/vms/golden-aarch64-darwin-vsock-candidate" \
  --identity "$HOME/.ssh/id_ed25519" --timeout 180
```

Final acceptance omits `--agent-source` so it tests the baked service. Keep its
`result.json` and command evidence. Actual Mac clone testing, baking, and final
acceptance are pending; Linux tests and Darwin cross-compilation cannot replace
those runtime checks.

### Recorded checks on this Linux host

[Linux result](validation/vsock-linux.json) records all 22 successful VM checks,
the guest image and tested binary hashes, the 1,170,432-byte CA fixture, and the evidence
directory. All disposable VM/image objects were removed. Ordinary Voom state,
source images, and all stashes were preserved.

- `just ci`: regular `check` passed; release snapshot stopped at the explicit
  Mac/Apple SDK requirement in `prepare-vfkit-release.sh`.
- `just nix-check`: passed separately, including the shared Python protocol check.
- Protocol, reconnect, report loss, native Darwin mount output, and lifecycle
  tests passed; focused Go tests also passed with the race detector.
- Darwin arm64 and amd64 Go cross-compilation passed.
- nixos-config's `macos-image` Python check and `macos-guest-launchd` generated
  service check passed using the local Voom override.
- The actual private Linux and Darwin VM configurations evaluated with local
  shared/Voom overrides. Linux has no control mount, uses a notify service, and
  orders egress setup after it. Darwin has the generated store startup guard.

- nixos-config's `voom-egress` NixOS VM test passed (attached, unattached, and
  invalid-manifest guests), including environment generation, browser trust,
  and failure handling. Its test VMs and helpers were cleaned up.
- The final Nix-built Voom package also passed a disposable QEMU start/stop/remove
  smoke test after the startup diagnostics changes.
