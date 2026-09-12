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
        gvproxySource = pkgs.callPackage ./nix/gvproxy-source.nix { };
        gvproxyLicenses = pkgs.callPackage ./nix/gvproxy-licenses.nix { inherit gvproxySource; };
        gvproxy = pkgs.callPackage ./nix/gvproxy.nix {
          inherit gvproxyLicenses gvproxySource;
          gvproxyNotice = ./nix/NOTICE.gvproxy;
        };
        voom = pkgs.buildGoModule {
          pname = "voom";
          inherit version;
          src = self;
          subPackages = [ "cmd/voom" ];
          vendorHash = "sha256-fxOjkVStDgZInA0/HEzi1/ZaI0XO+bc7sI94ok0QenY=";
          ldflags = [
            "-X github.com/mjrusso/voom/internal/cli.version=${version}"
            "-X github.com/mjrusso/voom/internal/cli.commit=${commit}"
            "-X github.com/mjrusso/voom/internal/cli.date=${date}"
          ];
          postInstall = ''
            ln -s ${gvproxy}/bin/gvproxy $out/bin/gvproxy
            mkdir -p $out/share/licenses
            ln -s ${gvproxy}/share/licenses/gvproxy $out/share/licenses/gvproxy
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
        packages.gvproxy = gvproxy;
        packages.gvproxy-source = gvproxySource;
        packages.voom = voom;
        packages.default = voom;
        apps.voom = flake-utils.lib.mkApp { drv = voom; };
        apps.default = self.apps.${system}.voom;
        devShells.ci = pkgs.mkShell { packages = ciPackages; };
        devShells.default = pkgs.mkShell {
          packages = ciPackages ++ [ pkgs.gopls pkgs.gotools ]
            ++ pkgs.lib.optionals pkgs.stdenv.isLinux [ pkgs.inotify-tools pkgs.libnotify ]
            ++ pkgs.lib.optionals pkgs.stdenv.isDarwin [ pkgs.terminal-notifier ];
        };
        checks.voom = voom;
        checks.gvproxy = gvproxy;
      });
}
