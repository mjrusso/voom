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
capability.

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

An HTTP request timeout does not establish whether a mutation executed. Voom
terminates the process when an expose outcome is unknown, rather than assuming
a later unexpose fences a delayed request. The versioned API's 400, 408, and 409
responses reject exposure before mutation; these failures do not require VM
shutdown. Other errors, including unexpected server errors, remain uncertain.

Remove the patch when an upstream release provides startup configuration,
host-only live control and observation, idempotent listeners, cancellation of
pending dials, and confirmed active-relay termination. Update the pinned source,
capability checks, and integration tests together.
