# Third-party licenses

Voom bundles these projects for virtualization and guest networking:

| Project | Role | License |
| --- | --- | --- |
| [vfkit](https://github.com/crc-org/vfkit) | Runs Linux and MacOS guests on MacOS hosts | [Apache License 2.0](https://github.com/crc-org/vfkit/blob/f02465693e0cec8bb3b92a9d8324644c63a2e7f2/LICENSE) |
| [vz](https://github.com/Code-Hex/vz) | Go bindings for Apple's Virtualization.framework | [MIT License](https://github.com/Code-Hex/vz/blob/v3.7.1/LICENSE) |
| [gvproxy](https://github.com/containers/gvisor-tap-vsock) | Provides guest networking on Linux and MacOS hosts | [Apache License 2.0](https://github.com/containers/gvisor-tap-vsock/blob/9cfc86f66679ef0feed0f20ba1df558fe2bef5c6/LICENSE) |

[Apple's Virtualization.framework](https://developer.apple.com/documentation/virtualization)
is supplied by MacOS. Voom itself is distributed under the [MIT License](../LICENSE).

All supported host packages include a modified gvproxy build. MacOS packages
also include a modified vfkit build with vz. The modification notices
are in [NOTICE.vfkit](../nix/NOTICE.vfkit) and
[NOTICE.gvproxy](../nix/NOTICE.gvproxy).

On Linux hosts, Voom uses a separately installed QEMU/KVM for virtualization;
see [host requirements](installation.md#host-requirements).

Full license texts and dependency notices are included with Voom's
distributions:

- All release archives contain `THIRD_PARTY_LICENSES.voom/` for dependencies
  linked into `voom`.
- All release archives contain `LICENSE.gvproxy`, `NOTICE.gvproxy`, and
  `THIRD_PARTY_LICENSES/` for gvproxy.
- MacOS release archives also contain `LICENSE.vfkit`, `NOTICE.vfkit`, and
  `THIRD_PARTY_LICENSES.vfkit/` for vfkit and vz.
- Nix installations provide Voom dependency licenses under
  `share/licenses/voom/` and gvproxy notices under `share/licenses/gvproxy/`
  in the Voom package. MacOS packages also provide vfkit and vz notices under
  `share/licenses/vfkit/`.

Release archives include the linked modification notices under `nix/`, along
with the root-level notices beside the helpers, and include `go.mod` and
`go.sum`.

Voom's Go module dependencies are recorded in [go.mod](../go.mod) and
[go.sum](../go.sum).
