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
        };
        ciPackages = with pkgs; [
          go
          git
          just
          goreleaser
          golangci-lint
          actionlint
        ];
      in {
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
      });
}
