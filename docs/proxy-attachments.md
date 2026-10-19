# Explicit HTTP proxy attachments

[README](../README.md) · [Command reference](commands/voom.md)

An explicit HTTP proxy attachment connects a Linux VM to an external proxy or
broker. MacOS guests do not support these attachments. The broker can apply
VM-specific policy and supply credentials without storing them in the guest.
Each attachment records a private backend Unix socket on the host:

```text
Guest application
├── uses the explicit proxy
│   └── 192.168.127.1:3128
│       └── Voom-managed listener on the guest gateway
│           └── TCP-to-Unix route
│               └── backend Unix socket assigned to this VM
│                   └── external proxy or adapter
│                       └── destination
│
└── connects directly
    └── ordinary direct networking
        └── destination
```

Voom manages the fixed listener on the guest gateway and the TCP-to-Unix
route. The external proxy or its adapter creates and listens on the backend
Unix socket. Voom also publishes `/run/voom/egress.json` and an optional CA
bundle at `/run/voom/egress-ca.pem`. Guest tooling can read the manifest to
configure applications. Voom does not run or manage the external proxy.

Guest applications must explicitly use the published proxy address. Voom does
not set `HTTP_PROXY`, `HTTPS_PROXY`, or application proxy settings. It also
does not block direct network access. Voom publishes the optional CA bundle
but does not install it. HTTP and HTTPS clients use the same proxy address.
HTTPS clients use HTTP CONNECT through that address.

Attachments require an image with `controlShare` capability. Check the
imported image before you configure the attachment:

```sh
voom image inspect linux-base
```

The output must show `capabilities: controlShare=true`. Images imported with
`--install-guest-helpers` have this capability. For custom images, see the
[Linux guest image contract](linux-guests.md#guest-image-contract). Start the
external proxy before setting or enabling an attachment; its backend socket
must accept connections. The examples below assume a VM named `agent-a`
already exists and is stopped.

```sh
voom config egress set agent-a \
  --backend-socket /run/credential-proxy/vm-01JXYZ.sock \
  --ca-cert /etc/credential-proxy/ca.pem
voom start agent-a
```

Inside the guest, an application can use the fixed proxy address directly:

```sh
curl --proxy http://192.168.127.1:3128 \
  --cacert /run/voom/egress-ca.pem \
  https://example.com/
```

The `--ca-cert` option publishes a CA bundle for proxies that terminate TLS.
Omit `--ca-cert` and `--cacert` when the proxy does not terminate TLS. Use
`voom config egress disable` and `voom config egress enable` to change proxy
access while the VM runs. To remove the saved attachment, stop the VM and use
`voom config egress clear`.

Example integrations:

- [Agent Vault on
  NixOS](https://github.com/mjrusso/nixos-config#voom-agent-vault) provides a
  complete Voom integration with per-VM identity, host-only proxy tokens, CA
  setup, and guest wrappers.
- [iron-proxy's CONNECT tunnel
  guide](https://docs.iron.sh/guides/socks5-connect) and [example
  configuration](https://github.com/paradigmxyz/iron-proxy/blob/main/iron-proxy.example.yaml)
  provide a starting point for explicit proxying, credential injection, and CA
  setup. Bridge its TCP tunnel listener to a private Unix socket before
  attaching it to Voom.

## Attachment state

This table shows the expected healthy state. `voom info` reports the observed
runtime state and any difference from the saved attachment.

| Saved attachment | VM state           | Private listener | Published manifest |
|------------------|--------------------|------------------|--------------------|
| None or disabled | Stopped or running | No               | No                 |
| Enabled          | Stopped            | No               | No                 |
| Enabled          | Running            | Yes              | Yes                |

Set and clear require a stopped VM with no surviving runtime. Enable and
disable also work while the VM runs. Run enable again to repair runtime drift.
If the runtime already matches the saved attachment, enable preserves the
listeners, tunnels, and runtime files. Disable closes the private listener,
pending dials, and active tunnels. Disable cannot retract requests that a
broker already accepted or guarantee cancellation of upstream work.

## External managers

External attachment managers should pass the immutable VM ID with every
change. They should first save the attachment in the disabled state:

```sh
voom info agent-a
VM_ID=30BD3BFA3D7C382195F82F603B5D4F11
voom config egress set agent-a \
  --expect-id "$VM_ID" \
  --disabled \
  --backend-socket "/run/credential-proxy/$VM_ID.sock" \
  --ca-cert /etc/voom-proxy/ca.pem
voom config egress enable agent-a --expect-id "$VM_ID"
```

`--expect-id` is available on set, clear, enable, and disable. Voom checks it
while it holds the VM state lock. Thus, a replacement VM with the same name
cannot receive the old attachment. `set --disabled` validates and reserves the
socket without publishing a guest route. Voom also validates the CA bundle.
The manager can then complete its policy checks and confirm that no emergency
hold applies before it enables the attachment.

Assign each backend socket and broker policy to the immutable VM ID shown by
`voom info`, not the mutable VM name. Voom reserves a socket across stopped
and disabled VMs in its state store. Operators must enforce uniqueness across
other state stores, users, and clients. Rename preserves attachments. Clones
omit them and report the new VM ID for a separate backend.

## Security boundaries

The external proxy or broker, not Voom, controls authentication, credential
injection, and policy enforcement. Proxy use is advisory, and direct
networking remains available. Requests that bypass the proxy receive no
broker-injected credentials.

Keep socket directories and broker policy under trusted host control. A shared
unauthenticated listener for all sockets does not preserve VM identity. Guest
headers and manifest contents do not authenticate a VM.

## Guest files and CA bundles

Voom publishes these files through `voom-control` when it enables an
attachment:

- `/run/voom/egress.json`: schema version 1, mode `explicit`, and `httpProxy` and
  `httpsProxy` set to `http://192.168.127.1:3128`.
- `/run/voom/egress-ca.pem`: optional validated public certificate bundle. The
  manifest includes `caCertificate` only when this file is configured.

Host copies live under `<runtime VM directory>/control/`. Files have mode
0644. Voom publishes the manifest after the CA file. Voom replaces the files
separately, so the update is not atomic. The metadata document always
advertises `controlShare.egressPath`, including when the saved configuration
has no attachment. Guests must choose how to consume the manifest and install
the public CA.

CA bundles must contain only X.509 certificate PEM blocks and at least one CA
certificate. Voom rejects private keys, other PEM blocks, and extraneous data.
A bundle can contain a maximum of 4 MiB. Each operation publishes the exact
bytes that it validated. `info` and `doctor` compare the published CA to the
current source. After the source changes, run enable or restart to update the
guest files.

## Observation and recovery

The `egress-config` column of `voom list` shows the saved attachment
(`enabled`, `disabled`, or `none`) and does not check the running VM. `voom
info` reports observed route state, connection count when available, and
configuration drift. `voom doctor` uses local socket probes and host API
queries. `voom doctor` sends no proxy requests or outbound internet traffic.
Disabled declarations can start without their backend, certificate, or
private-transport capability.

Disable persists the disabled declaration after removing runtime access. A
crash or persistence failure between those steps can leave the saved
attachment enabled. A later restart can restore access.

A failed enable can leave an enabled declaration with runtime access removed.
After you fix the reported cause, run enable again. If Voom cannot confirm
route removal, it tries to stop the VM runtime. Unconfirmed termination is an
error. Voom retains the process record and log paths for recovery.
