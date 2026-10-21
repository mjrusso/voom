{
  description = "voom local VM manager";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        version = pkgs.lib.removeSuffix "\n" (builtins.readFile ./VERSION);
        commit = self.rev or self.dirtyRev or "dirty";
        modified = self.lastModifiedDate or null;
        date =
          if modified == null then "unknown"
          else "${builtins.substring 0 4 modified}-${builtins.substring 4 2 modified}-${builtins.substring 6 2 modified}T${builtins.substring 8 2 modified}:${builtins.substring 10 2 modified}:${builtins.substring 12 2 modified}Z";
        vfkitSource = pkgs.callPackage ./nix/vfkit-source.nix { };
        vfkitLicenses = pkgs.callPackage ./nix/vfkit-licenses.nix { inherit vfkitSource; };
        vfkit = pkgs.callPackage ./nix/vfkit.nix { inherit vfkitSource vfkitLicenses; };
        gvproxySource = pkgs.callPackage ./nix/gvproxy-source.nix { };
        gvproxyLicenses = pkgs.callPackage ./nix/gvproxy-licenses.nix { inherit gvproxySource; };
        gvproxy = pkgs.callPackage ./nix/gvproxy.nix {
          inherit gvproxyLicenses gvproxySource;
          gvproxyNotice = ./nix/NOTICE.gvproxy;
        };
        macosIdentity = pkgs.stdenv.mkDerivation {
          pname = "voom-macos-identity";
          inherit version;
          src = ./native/macos-identity;
          buildPhase = ''
            $CC -fobjc-arc -Wall -Wextra -Werror -mmacosx-version-min=12.0 \
              -framework Foundation -framework Virtualization main.m -o voom-macos-identity
          '';
          doCheck = true;
          checkPhase = "./voom-macos-identity --self-test";
          installPhase = "install -Dm755 voom-macos-identity $out/bin/voom-macos-identity";
        };
        hasMacOSIdentity = system == "aarch64-darwin";
        voom = pkgs.buildGoModule {
          pname = "voom";
          inherit version;
          src = self;
          subPackages = [ "cmd/voom" ];
          nativeBuildInputs = [ pkgs.installShellFiles ];
          vendorHash = "sha256-fxOjkVStDgZInA0/HEzi1/ZaI0XO+bc7sI94ok0QenY=";
          ldflags = [
            "-X github.com/mjrusso/voom/internal/buildinfo.Version=${version}"
            "-X github.com/mjrusso/voom/internal/buildinfo.Commit=${commit}"
            "-X github.com/mjrusso/voom/internal/buildinfo.Date=${date}"
          ];
          postInstall = ''
            installShellCompletion --cmd voom \
              --bash <($out/bin/voom completion bash) \
              --fish <($out/bin/voom completion fish) \
              --zsh <($out/bin/voom completion zsh)
            ln -s ${gvproxy}/bin/gvproxy $out/bin/gvproxy
            ${pkgs.lib.optionalString pkgs.stdenv.isDarwin "ln -s ${vfkit}/bin/vfkit $out/bin/vfkit"}
            ${pkgs.lib.optionalString hasMacOSIdentity "ln -s ${macosIdentity}/bin/voom-macos-identity $out/bin/voom-macos-identity"}
            mkdir -p $out/share/licenses
            ln -s ${gvproxy}/share/licenses/gvproxy $out/share/licenses/gvproxy
            ${pkgs.lib.optionalString pkgs.stdenv.isDarwin "ln -s ${vfkit}/share/licenses/vfkit $out/share/licenses/vfkit"}
          '';
        };
        ciPackages = with pkgs; [
          go
          git
          just
          goreleaser
          go-licenses
          golangci-lint
          actionlint
          gvproxy
        ];
      in {
        packages = {
          vfkit-source = vfkitSource;
          inherit gvproxy voom;
          gvproxy-source = gvproxySource;
          default = voom;
        } // pkgs.lib.optionalAttrs pkgs.stdenv.isDarwin { inherit vfkit; };
        apps.voom = flake-utils.lib.mkApp { drv = voom; };
        apps.default = self.apps.${system}.voom;
        devShells.ci = pkgs.mkShell { packages = ciPackages; };
        devShells.default = pkgs.mkShell {
          packages = ciPackages ++ [ pkgs.gopls pkgs.gotools ]
            ++ pkgs.lib.optionals pkgs.stdenv.isLinux [ pkgs.inotify-tools pkgs.libnotify ]
            ++ pkgs.lib.optionals pkgs.stdenv.isDarwin [ pkgs.terminal-notifier ];
        };
        checks.voom = voom;
        checks.voom-completions = pkgs.callPackage ./nix/completion-test.nix { inherit voom; };
        checks.gvproxy = gvproxy;
        checks.vfkit-source = pkgs.runCommand "check-vfkit-source" { nativeBuildInputs = [ pkgs.go ]; } ''
          export GOCACHE=$TMPDIR/go-cache
          export CGO_ENABLED=0
          cp -R ${vfkitSource} source
          chmod -R u+w source
          cd source
          go test -mod=vendor ./pkg/cmdline ./pkg/rest ./pkg/util
          go test -mod=vendor ./pkg/config -run 'TestVirtioFsReadOnlyRoundTrip|TestJSON'
          touch $out
        '';
      });
}
