# Third-party licenses

Voom uses these projects to run and connect MacOS guests:

| Project | Role | License |
| --- | --- | --- |
| [vfkit](https://github.com/crc-org/vfkit) | Runs the virtual machine | [Apache License 2.0](https://github.com/crc-org/vfkit/blob/f02465693e0cec8bb3b92a9d8324644c63a2e7f2/LICENSE) |
| [vz](https://github.com/Code-Hex/vz) | Go bindings for Apple's Virtualization.framework | [MIT License](https://github.com/Code-Hex/vz/blob/v3.7.1/LICENSE) |
| [gvproxy](https://github.com/containers/gvisor-tap-vsock) | Provides guest networking | [Apache License 2.0](https://github.com/containers/gvisor-tap-vsock/blob/9cfc86f66679ef0feed0f20ba1df558fe2bef5c6/LICENSE) |

[Apple's Virtualization.framework](https://developer.apple.com/documentation/virtualization)
is supplied by MacOS. Voom itself is distributed under the [MIT License](../LICENSE).

Voom bundles modified builds of vfkit, vz, and gvproxy. The modification notices
are in [NOTICE.vfkit](../nix/NOTICE.vfkit) and
[NOTICE.gvproxy](../nix/NOTICE.gvproxy).

Full helper license texts and dependency notices are included with Voom's
distributions:

- MacOS release archives contain `LICENSE.vfkit`, `NOTICE.vfkit`, and
  `THIRD_PARTY_LICENSES.vfkit/` for vfkit and vz, plus `LICENSE.gvproxy`,
  `NOTICE.gvproxy`, and `THIRD_PARTY_LICENSES/` for gvproxy.
- Nix installations provide the helper notices under `share/licenses/vfkit/`
  and `share/licenses/gvproxy/` in the Voom package.

Voom's Go module dependencies are recorded in [go.mod](../go.mod) and
[go.sum](../go.sum).
