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
        voom = pkgs.buildGoModule {
          pname = "voom";
          version = "0.0.0";
          src = self;
          subPackages = [ "cmd/voom" ];
          vendorHash = "sha256-fxOjkVStDgZInA0/HEzi1/ZaI0XO+bc7sI94ok0QenY=";
          ldflags = [
            "-X github.com/mjrusso/voom/internal/cli.version=0.0.0"
            "-X github.com/mjrusso/voom/internal/cli.commit=${self.rev or "dirty"}"
            "-X github.com/mjrusso/voom/internal/cli.date=unknown"
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
