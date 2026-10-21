# Contributing

Run the commands below from a source checkout of this repository.

Use Nix for the dev shell:

```sh
nix develop
```

The `default` shell includes everything in the CI shell (`go`, `git`, `just`,
`goreleaser`, `golangci-lint`, `actionlint`, and the pinned `gvproxy`) plus
`gopls` and platform-specific notification helpers. `nix develop .#ci` is the minimal shell that CI uses.

On a Mac with Apple's SDK, run the full check suite before committing:

```sh
just ci
```

On Linux, run `just check` and `nix flake check --show-trace`. Release archive
creation and native signing checks require a Mac and run on MacOS CI.

Build a local binary with `just build` and try `./bin/voom`. On MacOS this also
builds the identity helper (Apple Silicon) and the pinned, patched vfkit through
Nix. vfkit is ad-hoc signed with its virtualization entitlement.

Voom patches vfkit v0.6.4. `nix/vfkit-source.nix` pins the upstream commit and
vendors dependencies; patches under `nix/patches/` extend vfkit and Code-Hex/vz.
Nix packages and release archives use the same source. The share patch adds host-side
read-only directory exports and the `virtiofs-readonly-v1` capability. Release
preparation uses Apple's toolchain selected by `xcode-select` and ad-hoc signs
both Darwin architectures.

The `gui-branding-v1` capability adds per-VM menu labels and Dock visibility
control. Voom sets the app name shown by MacOS through a per-VM app bundle:
it copies vfkit into the bundle and writes the VM name into localized
`InfoPlist.strings` resources. The base names in `Info.plist` match the bundle
filename so the Dock uses the localized name. Voom then applies an ad-hoc
signature, preserving vfkit's entitlements. The release smoke check verifies
the bundled vfkit's capabilities and signature.

Voom embeds `assets/voom.icns` and writes it to `Contents/Resources` before
signing. `CFBundleIconFile` selects it as the app icon. Regenerate the `.icns`
file when changing the icon artwork.

In a logged-in graphical MacOS session, test two running MacOS guests. Check
that the Dock and menu bar show `Voom: <vm name>` for each guest and that both
Dock entries use the Voom icon. Close their windows and confirm that their
Dock and Command-Tab entries disappear while the VMs keep running. Use
`voom gui` to restore each window and its Dock entry.
Check that minimizing a window or switching apps keeps its Dock entry. Test
that Quit requests guest shutdown and Force Quit terminates the VM.

To test app-name registration without a guest VM, run this command in a
logged-in MacOS session with Go and Apple's SDK installed:

```sh
bash scripts/test-macos-app-name.sh
```

The script first confirms that setting the process name alone leaves the
registered app name as `vfkit`. It then uses Voom's bundle builder to create
two apps and checks their registered app names and filesystem display names.
The filesystem check catches cases where the menu bar uses the intended name
but the Dock falls back to the bundle filename. The script also checks
MacOS icon decoding, signatures, virtualization entitlements, process
identification under paths containing spaces, long VM names, and name changes
when a bundle is replaced.

To check both names for a running VM, use its vfkit PID and VM name:

```sh
bash scripts/check-macos-app-name.sh <vfkit-pid> <vm-name>
```

Use disposable directories so local tests do not touch your normal voom state:

```sh
export VOOM_CONFIG_DIR="$PWD/.voom-test/config"
export VOOM_STATE_DIR="$PWD/.voom-test/state"
export VOOM_CACHE_DIR="$PWD/.voom-test/cache"
export VOOM_RUNTIME_DIR="$PWD/.voom-test/runtime"

./bin/voom debug paths
./bin/voom doctor
```

To exercise the binary against a real VM, import an image and then create and
start a VM. See [Getting Started](README.md#getting-started) for a full example;
the following works on an ARM64 Mac with a Debian image:

```sh
./bin/voom image import debian13 ./debian-13-generic-arm64.raw \
  --arch aarch64-linux --format raw \
  --ssh-user debian --install-guest-helpers
./bin/voom create deb --image debian13 --driver vfkit
./bin/voom start deb
```

To clean up disposable test state:

```sh
./bin/voom stop deb
./bin/voom remove deb --force
./bin/voom image remove debian13 --force
rm -rf .voom-test
```

## justfile targets

| Target                        | Purpose                                                                   |
|-------------------------------|---------------------------------------------------------------------------|
| `just build`                  | Build the `voom` binary into `bin/`.                                      |
| `just test`                   | Run `go test ./...`.                                                      |
| `just vet`                    | Run `go vet ./...`.                                                       |
| `just lint`                   | Run `golangci-lint` and `actionlint`.                                     |
| `just docs`                   | Regenerate `docs/commands/` from the Cobra command tree.                  |
| `just tidy-check`             | Verify `go mod tidy` produces no diff.                                    |
| `just scripts-check`          | Syntax-check shell scripts under `scripts/`.                              |
| `just smoke`                  | Build and run `voom version` / `voom --help`.                             |
| `just release-snapshot-check` | Snapshot release: `goreleaser`, verify archives, smoke-test an archive.   |
| `just nix-check`              | Run `nix flake check`, build `.#voom`, and run the resulting binary.      |
| `just release-prep vX.Y.Z`    | Validate release notes and run the local release dry run.                 |
| `just check`                  | Aggregate gate: docs/vet/test/lint/tidy/scripts/smoke + docs drift check. |
| `just ci`                     | Full local gate: `check`, `release-snapshot-check`, and `nix-check`.      |
| `just dev`                    | Alias for `just check`.                                                   |
| `just clean`                  | Remove `bin/`, `dist/`, and `result`.                                     |

## Generated command docs

`docs/commands/` is generated by `cmd/gen-docs/main.go` from the Cobra command
tree (`DisableAutoGenTag = true`). Regenerate with `just docs`; CI fails if the
generated tree drifts from what is committed.

## Internal package boundaries

Newly factored low-level driver, host, process, share, and forward behavior
should live in the owning package rather than being inlined into Cobra command
handlers.

- `internal/cli` — Cobra command construction, argument validation, help,
  output format selection, version command.
- `internal/doctor` — `voom doctor` checks and state diagnostics.
- `internal/vm` — orchestration for create, start, stop, list, resources,
  forward, share, and USB assignments. Coordinates other packages but avoids
  low-level process details.
- `internal/state` — directory resolution, typed state loading/saving, schema
  version checks, VM/image path derivation, recoverable writes, locking, and
  runtime path layout. Normal command loading uses `state.json`; `voom doctor`
  performs the broader object-directory scan.
- `internal/image` — image disk inspection and direct-boot extraction.
- `internal/host` — host OS/architecture detection, image system/format
  derivation, executable lookup, KVM detection, environment-derived paths.
- `internal/process` — detached process launching, process-record creation and
  validation, PID reuse detection, signal handling.
- `internal/gvproxy` — HTTP-over-Unix-socket gvproxy forwarder client.
- `internal/bootstrap` — cloud-init NoCloud seed image generation.
- `internal/driver/qemu` — Linux/QEMU launch args, monitor protocol, graceful
  shutdown, virtiofs wiring.
- `internal/driver/vfkit` — Darwin/vfkit launch args, REST control socket,
  graceful shutdown, native virtio-fs wiring.
- `internal/share` — share declarations, validation, guest mount scripts, and
  virtiofsd argument construction.
- `internal/usb` — Linux USB inventory, topology parsing, and device-access
  checks.
- `internal/forward` — manual forward declarations, auto-forward state and
  reconciliation, bind-address normalization, gvproxy expose/unexpose plans.

Driver packages build launch arguments and provide control APIs for
`internal/vm`. Do not put QEMU or vfkit launch argument construction in command
handlers.

## Test conventions

Use Go's standard `testing` package. Prefer table tests for validation. Inject
fake process runners, filesystems, and command inputs where useful. Keep tests
deterministic; don't mutate real VMs or the developer's machine.

Areas that should stay covered by unit tests:

- root command surface, flag validation, and unsupported-output rejection;
- `version` text and JSON output;
- VM/image/share name validation;
- size parsing;
- host/image format detection;
- state path derivation, JSON encode/decode, and schema-version checks;
- generated VM/image IDs and name-to-ID index maintenance;
- state recovery diagnostics;
- global and per-VM lock ordering;
- stopped-VM resource mutation and running-VM rejection;
- image import validation, inspection output, removal blocking, and capability
  gating;
- forward declaration validation and SSH/forward port conflict behavior;
- persisted port reservations across stopped VMs;
- guest `ports.json` parsing and stale-file handling;
- auto-forward enable/disable state changes and fake gvproxy reconciliation;
- all-VM `forward list` output;
- process-record validation by process kind and PID reuse;
- `remove` confirmation and `--force`;
- gvproxy request/response parsing.

Fixture tests live under `testdata/state/` and mirror the v1 state layout
(`state.json`, `images/<image-id>/image.json`,
`vms/<vm-id>/vm.json`). Read-only fixture commands look like:

```sh
VOOM_STATE_DIR="$PWD/testdata/state" go run ./cmd/voom list
VOOM_STATE_DIR="$PWD/testdata/state" go run ./cmd/voom info scratch
VOOM_STATE_DIR="$PWD/testdata/state" go run ./cmd/voom image inspect nixos
```

For gvproxy tests, use a fake HTTP server bound to a Unix socket with
`net.Listen("unix", path)` and `http.Serve`. Disposable real-VM testing must
use separate directories:

```sh
export VOOM_STATE_DIR="$HOME/voom-test/state"
export VOOM_CACHE_DIR="$HOME/voom-test/cache"
export VOOM_RUNTIME_DIR="$HOME/voom-test/runtime"
```

## Nix package

`flake.nix` exposes `packages.voom` built with `pkgs.buildGoModule`:

- `pname = "voom"`, `subPackages = [ "cmd/voom" ]`;
- version ldflags injected via package path
  `github.com/mjrusso/voom/internal/cli`;
- sibling helper symlinks for gvproxy on all platforms, vfkit on Darwin, and
  the Apple identity helper on Darwin arm64, with bundled helper licenses.

After Go dependency changes, update `vendorHash` in `flake.nix`:

1. Temporarily set `vendorHash = lib.fakeHash;`.
2. Run `nix build .#voom`.
3. Copy the fixed-output hash from the Nix error message into `flake.nix`.

Two dev shells are exposed:

- `devShells.ci` — minimal: `go`, `git`, `just`, `goreleaser`, `golangci-lint`,
  `actionlint`, and Voom's pinned `gvproxy` build.
- `devShells.default` — CI shell plus `gopls` and notification tools
  (`inotify-tools` / `libnotify` on Linux; `terminal-notifier` on Darwin).

## CI

GitHub Actions installs Nix in every job. Three jobs run on pull requests and
pushes to `main`:

1. `check` — `nix develop .#ci -c just check` on Ubuntu and MacOS.
2. `release-snapshot` — `nix develop .#ci -c just release-snapshot-check`.
   This runs on MacOS to compile and sign vfkit and the native identity helper.
3. `nix` — `nix develop .#ci -c just nix-check` on Ubuntu and MacOS. The
   recipe runs `nix flake check --show-trace`, `nix build .#voom`, asserts
   `result/bin/voom` is executable, and runs `nix run .#voom -- version`.

Common workflow setup: `actions/checkout@v5`,
`nixbuild/nix-quick-install-action`, `nix-community/cache-nix-action` (keyed on
`hashFiles('**/*.nix', 'flake.lock')`), `permissions: contents: read`.

## Changelog

`CHANGELOG.md` is curated by hand. Keep an `## Unreleased` section at the top
and add user-facing changes there as they land. Internal-only maintenance can
be omitted unless it affects installation, release artifacts, compatibility, or
operator workflows.

Before tagging a release, move the relevant `Unreleased` entries under a
versioned section:

```md
## v0.1.0 - 2026-05-27

- Add x.
```

Release section headers must match `## vX.Y.Z - YYYY-MM-DD`. The release
workflow fails if the pushed tag does not have a matching non-empty changelog
section. That section is passed to GoReleaser as the GitHub release notes.

## Release process

Releases are tag-driven. To cut `vX.Y.Z`, start from an up-to-date `main`, set
`VERSION` to `X.Y.Z`, promote the changelog entries from `Unreleased` to
`## vX.Y.Z - YYYY-MM-DD`, commit those changes, and run the local release prep:

```sh
git switch main
git pull --ff-only
$EDITOR CHANGELOG.md
printf '%s\n' X.Y.Z > VERSION
git add CHANGELOG.md VERSION
git commit -m "Prepare vX.Y.Z release"
just release-prep vX.Y.Z
git push origin main
```

If release prep passes and `main` is pushed, create and push an annotated tag:

```sh
git tag -a vX.Y.Z -m "vX.Y.Z"
git push origin vX.Y.Z
```

Tags matching `v*` trigger `.github/workflows/release.yml`, which:

- checks out with `fetch-depth: 0`;
- installs and caches Nix;
- verifies the tag commit is reachable from `origin/main`;
- runs `just check` (snapshot and nix checks are skipped here — the real
  release build below subsumes them);
- verifies the matching `CHANGELOG.md` section and passes it to GoReleaser as
  release notes;
- runs `goreleaser release --clean --release-notes ...` with `GITHUB_TOKEN`;
- runs `scripts/verify-release-archives.sh dist` to verify archive contents
  and `sha256` checksums;
- runs `scripts/smoke-release.sh dist` to smoke-test a release archive.

GoReleaser builds `voom` and the patched `gvproxy` (`CGO_ENABLED=0`) for
`darwin` and `linux` on `amd64` and `arm64`. The gvproxy source is prepared by
Nix from the pinned upstream revision and local patch. GoReleaser creates
`tar.gz` archives named `<project>_<os>_<arch>` (with `amd64` rewritten to
`x64`) and includes the README, guides, command reference, contributing
instructions, and their linked local assets.
`checksums.txt` uses `sha256`. The snapshot template is
`{{ incpatch .Version }}-next`.

Before major releases, run the automated checks and a manual live-VM checklist
with disposable `VOOM_*` directories: import an image, create/start multiple
VMs, verify SSH and forwards, inspect logs and diagnostics,
stop/restart/rename/remove them, and confirm no test QEMU or gvproxy processes
remain.

Expected release archives:

```text
dist/voom_darwin_arm64.tar.gz
dist/voom_darwin_x64.tar.gz
dist/voom_linux_arm64.tar.gz
dist/voom_linux_x64.tar.gz
dist/checksums.txt
```

Each archive contains `LICENSE`, `LICENSE.gvproxy`, `NOTICE.gvproxy`,
`THIRD_PARTY_LICENSES`, `README.md`, `CHANGELOG.md`, `CONTRIBUTING.md`,
`docs/`, `assets/voom.png`, `nix/GVPROXY.md`, `voom`, and `gvproxy`.
Darwin archives also contain vfkit and its license notices; Darwin arm64 includes
`voom-macos-identity`. The native preparation scripts build those helpers before
GoReleaser assembles the archives. Verify with `sha256sum -c checksums.txt`.

## Egress transport development

Both shells include the [pinned gvproxy build](nix/GVPROXY.md). Its Nix check
runs relay tests with the race detector and a two-process integration test using
simulated guest network stacks. No VM image is downloaded. Changes to the patch
must keep this gate passing; ordinary Voom unit tests use temporary sockets and
fake host-control APIs.
