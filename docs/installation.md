# Installation

[README](../README.md) · [Linux guests](linux-guests.md) ·
[MacOS guests](macos-guests.md)

- [Host requirements](#host-requirements)
- [Install a release build](#install-a-release-build)
- [Install with Nix](#install-with-nix)
- [Build from source](#build-from-source)

> [!NOTE]
>
> Release and Nix installations include Voom's gvproxy build. Apple Silicon
> MacOS packages also include vfkit and the Apple identity helper. Other host
> dependencies, such as QEMU and SSH, are not included. See
> [host requirements](#host-requirements).

## Host requirements

- **Linux**: `/dev/kvm` access through [KVM](https://linux-kvm.org/),
  [QEMU](https://www.qemu.org/) (`qemu-system-<arch>`, `qemu-img`), and
  [OpenSSH](https://www.openssh.com/) (`ssh`).
- **MacOS**: an Apple Silicon Mac with [OpenSSH](https://www.openssh.com/)
  (`ssh`, included with MacOS).
  MacOS guests additionally require MacOS 27 or later on both the host and
  guest, with a logged-in graphical session on the host; see the
  [MacOS guest guide](macos-guests.md).

Optional integrations: [virtiofsd](https://gitlab.com/virtio-fs/virtiofsd)
(Linux/QEMU shares), [Nix](https://nixos.org/download/) and
[`nixos-rebuild`](https://nixos.org/manual/nixos/stable/#sec-changing-config)
(for `voom nixos switch`), and [Git](https://git-scm.com/) (NixOS switch
metadata). USB passthrough from Linux hosts requires a QEMU build
with XHCI and libusb `usb-host` support, and PCI passthrough requires a QEMU
build with VFIO `vfio-pci` support.

Install any missing host tools with your OS package manager.

## Install a release build

Download a prebuilt archive for your platform from the [GitHub releases
page](https://github.com/mjrusso/voom/releases), verify it against
`checksums.txt`, and extract it into a directory on your `PATH`. Keep `gvproxy`
in the same directory as `voom`. On Apple Silicon MacOS, also keep `vfkit` and
`voom-macos-identity` beside `voom`.

> [!IMPORTANT]
>
> The MacOS release binaries are not notarized. If your download is quarantined,
> Gatekeeper may show _"Apple could not verify ... is free of malware"_. Clear
> the quarantine attribute from the extracted binaries before running them:
>
> ```sh
> xattr -d com.apple.quarantine ./voom ./gvproxy ./vfkit ./voom-macos-identity
> ```
>
> vfkit is ad-hoc signed with the virtualization entitlement; no Developer ID
> certificate is required.

## Install with Nix

```sh
nix profile install github:mjrusso/voom
```

Or run directly without installing:

```sh
nix run github:mjrusso/voom -- version
```

To use `voom` from another flake:

```nix
{
  inputs.voom.url = "github:mjrusso/voom";

  outputs = { nixpkgs, voom, ... }:
    let
      system = "x86_64-linux";
      pkgs = import nixpkgs { inherit system; };
    in {
      devShells.${system}.default = pkgs.mkShell {
        packages = [ voom.packages.${system}.default ];
      };
    };
}
```

In NixOS or Home Manager configs, add `voom.packages.${pkgs.system}.default`
to `environment.systemPackages` or `home.packages` after passing the flake
input to the module.

## Build from source

For instructions on building from source, see
[CONTRIBUTING.md](../CONTRIBUTING.md).
