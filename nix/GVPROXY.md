# Private gateway transport dependency

Voom maintains `gvproxy.nix` and `patches/gvproxy-gateway-forward.patch` until
upstream provides the complete `gateway-forward-v1` capability. The packaging
owner is the Voom repository maintainer. The patch is based on gvisor-tap-vsock
v0.8.9, commit `9cfc86f66679ef0feed0f20ba1df558fe2bef5c6`.

Voom-owned implementations and tests live in `internal/gvproxy/source/`, with
paths relative to the upstream tree. Their `ignore` build constraint keeps
this partial upstream source out of Voom's Go module. `gvproxy-source.nix`
removes that constraint when installing the files into the patched upstream
tree. Edit those files directly; patches contain upstream integration changes.

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
for development. Voom requires `guest-isolation-v1`, `blocked-subnets-v1`,
and `forward-recovery-v3`.

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
Failed closes retain their forwarding-table entry so later requests can retry.
TCP proxy shutdown reports listener close errors. A failed removal is retried
even when a snapshot omits the listener; Voom retains its reservation until
closure is confirmed.

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

`internal/gvproxy/forwarder/mutation_outcomes.go` implements request tracking.
Nix installs this file unchanged into gvproxy, and Voom's test fixtures import
the same package. Its upstream protocol tests live in
`internal/gvproxy/source/pkg/services/forwarder/`.
`gvproxy-mutation-outcomes.patch` adds the integration points.

The forwarder tracks requests carrying an
`X-Voom-Forward-ID` header. `POST /services/forwarder/ids?count=N` allocates a
range of ordered IDs and returns its first ID. Allocation advances a counter;
it does not retain a record for each ID.

`GET /services/forwarder/outcome?id=...` returns 202 while the handler is pending,
200 with its original HTTP status and body after completion, or 404 for an
unknown ID. Repeating the same ID with an identical action and body returns 202
while pending, or the original HTTP status and body after completion. A changed
action or body is rejected with HTTP 422.

`POST /services/forwarder/acknowledge?id=...` acknowledges a durably recorded
result. It is idempotent and rejects pending requests. A retirement watermark
advances past acknowledged requests without crossing an unacknowledged request.
Records at or below the watermark are deleted. Both queries and submissions for
retired IDs return HTTP 410 with `X-Voom-Forward-Outcome: retired`; those IDs
can never execute again, even if they had not reached gvproxy before retirement.

The process retains at most 256 request records, including acknowledgements
waiting behind an older request. When full, it rejects new mutations with HTTP
429 before execution. These host-only endpoints report `forward-recovery-v3`.

A forwarding-table snapshot alone cannot rule out a delayed expose. Voom retries
uncertain submissions with their original IDs and verifies the table after
reported success. It records request IDs, operations, targets, manual add/remove
intent, the full automatic forwarding plan, and the gvproxy process identity
before sending requests. A rejected manual add is removed from saved declarations
during journal completion, including recovery after the CLI exits.
The journal has one manual intent, which must match its request's action and
endpoint. Recovery validates this before changing declarations. Completed results
retain their outcome classification so recovery does not reinterpret HTTP errors.
Voom saves completion before acknowledging it, then records acknowledgement
progress so later attempts skip finished requests. Verification shares one
forwarding-table snapshot across all requests. Once all submitted requests are
fenced, confirmed rejections do not block further forwarding changes. Rejected
removals keep the existing listener reserved.
Recovery can safely resubmit an uncertain ID, including after a crash between
saving it and sending it. If that ID has retired, recovery checks the forwarding
table and allocates a new ID only if another change is needed.

For manual or automatic port-forward changes on a running VM, failed recovery
leaves the guest and gvproxy running. Voom keeps the recovery journal, reserves
the affected ports, and blocks further forwarding changes until confirmation.
Automatic forwarding retries unresolved requests through its watcher when
enabled. Journals without the
required request or commit details require a stop/start. Private gateway
recovery follows the shutdown behavior described above; failed-startup cleanup
can also stop the VM.
