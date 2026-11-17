# Private gateway transport dependency

Voom maintains `gvproxy.nix` and `patches/gvproxy-gateway-forward.patch` until
upstream provides the complete `gateway-forward-v1` capability. The packaging
owner is the Voom repository maintainer. The patch is based on gvisor-tap-vsock
v0.8.9, commit `9cfc86f66679ef0feed0f20ba1df558fe2bef5c6`.

Release archives and the default Nix package include this build. To build it
separately during development:

```sh
nix build .#gvproxy
export VOOM_GVPROXY="$PWD/result/bin/gvproxy"
```

Binary distributions include the upstream Apache-2.0 license, a modification
notice, and licenses and notices for linked dependencies. Release preparation
uses `go-licenses` against each release target. The Nix package installs the
same material under `share/licenses/gvproxy`.

Both Voom development shells include this build. `nix flake check` builds it
and runs race-enabled relay tests and a two-process integration test. The test
connects independent guest network stacks to two real gvproxy processes through
QEMU network sockets; it needs neither a VM image nor a hypervisor.

The default Nix package installs `voom` and `gvproxy` together. The separate
`packages.${system}.gvproxy` output and `VOOM_GVPROXY` override remain available
for development. Voom rejects binaries without its `guest-isolation-v1`
and `blocked-subnets-v1` capabilities.

The patch exposes JSON capability reporting with `-capabilities` and
`GET /services/gateway-forward/capabilities`. Startup accepts repeated
`-gateway-forward` arguments containing a JSON object with `local` and `target`.
The JSON representation preserves spaces, equals signs, and other supported
filesystem-path characters without a shell or URL parser.

The patched network removes gvproxy's guest-to-host-loopback mapping. Guests
cannot reach host `127.0.0.0/8` or `::1` services through
`192.168.127.254`, `host.containers.internal`, or `host.docker.internal`.
Voom's host-to-guest SSH, declared-forward, and automatic-forward listeners do
not use that mapping.

The patched host forwarder treats removal of an absent listener as success.
Cleanup can retry without matching gvproxy error text.

The host Unix control mux exposes `expose`, `unexpose`, and `all` below
`/services/gateway-forward/`. These handlers are absent from the guest mux,
services mux, and host TCP control listeners. Listener removal waits for the
accept worker, pending backend dials, and relays to terminate. Removed addresses
remain reserved against the ordinary TCP forwarding fallback for the lifetime
of the process.

An HTTP request timeout does not establish whether a mutation executed. For
private gateway attachments, Voom stops the VM and gvproxy if an expose request's
result is unknown, or if unexpose fails. A later unexpose cannot fence a delayed
request. The versioned API's 400, 408, and 409 responses reject exposure
before mutation; these failures do not require VM shutdown. Other errors,
including unexpected server errors, leave the result unknown.

Remove the patch when an upstream release provides startup configuration,
host-only live control and observation, idempotent listeners, cancellation of
pending dials, and confirmed active-relay termination. Update the pinned source,
capability checks, and integration tests together.

UDP forwards use a separate guest connection for each host client.
`gvproxy-udp-forward.patch` preserves empty datagrams and prevents new sessions
after removal. The capability reports include `udp-forward-v1`, which Voom
requires to install UDP forwards.

`gvproxy-forward-outcomes.patch` reports rejected expose requests as HTTP 409
only when no listener was installed. Voom keeps affected ports reserved while it
cannot confirm whether a listener was installed or removed.

`gvproxy-blocked-subnets.patch` adds repeatable `-block-subnet CIDR` and
`-allow-destination IP:PORT` flags. YAML uses `stack.blockedSubnets` and
`stack.allowedDestinations`; flags append to these lists. TCP and UDP
forwarders check the destination after NAT translation. Exceptions allow one
IP and port; loopback and link-local restrictions still apply. The build
reports `blocked-subnets-v1`.

`GET /services/network/policy` reports the subnet blocks and destination
exceptions installed in the TCP and UDP forwarders. It is available only on
the host Unix control socket. The capabilities response remains a version map.
Voom uses the policy report to detect missing blocks and settings that require
a VM restart.

`gvproxy-mutation-outcomes.patch` tracks host forwarder requests carrying an
`X-Voom-Forward-ID` header. `GET /services/forwarder/outcome?id=...` returns 202
while that handler is pending, 200 with its original HTTP status and body after
completion, or 404 for an unknown or evicted ID. The process retains up to 256
outcomes and only evicts completed entries. While an ID remains cached, reusing
it is rejected without repeating the mutation. This host-only endpoint reports
when the original handler has finished. A forward table snapshot alone cannot
rule out a delayed expose. After an uncertain response, Voom queries the outcome
up to three times without resending the mutation and verifies the table after
reported success.

For manual or automatic port-forward changes on a running VM, failed recovery
leaves the guest and gvproxy running. Voom keeps the recovery journal, reserves
the affected ports, and blocks further forwarding changes until the VM is stopped
and started. Private gateway recovery follows the shutdown behavior described
above; failed-startup cleanup can also stop the VM.

Commands log each recovery attempt. The automatic forwarding watcher logs an
error when it first occurs or changes, including errors checking whether the VM
process has exited. After driver exit, cleanup waits for MacOS storage locks to
be released before emitting a `vm stop` event with `reason=driver exited`.

Voom-owned helper implementations and tests live under `internal/gvproxy/source`
and `internal/gvproxy/forwarder`. `gvproxy-source.nix` installs them after
applying patches to upstream integration points.
