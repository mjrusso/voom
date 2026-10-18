{ applyPatches, buildGoModule, fetchFromGitHub, runCommand, patch }:
let
  upstream = fetchFromGitHub {
    owner = "crc-org";
    repo = "vfkit";
    rev = "f02465693e0cec8bb3b92a9d8324644c63a2e7f2";
    hash = "sha256-ay1qzVJpflxszoF6GkQymM/NkEazbC8j5W5lJSvhzX0=";
  };
  modules = (buildGoModule {
    pname = "vfkit";
    version = "0.6.4";
    src = upstream;
    vendorHash = "sha256-c+ZiAgy8b0ucc8gHEUDZvgAWerP3fX9crI3XI75x8M4=";
  }).goModules;
  vendored = runCommand "vfkit-0.6.4-vendored" { } ''
    mkdir $out
    cp -R ${upstream}/. $out/
    cp -R ${modules} $out/vendor
  '';
in
applyPatches {
  name = "vfkit-0.6.4-voom.1-source";
  src = vendored;
  nativeBuildInputs = [ patch ];
  patches = [ ./patches/vfkit-gui.patch ./patches/vfkit-shares.patch ];
  postPatch = ''
    patch -d vendor/github.com/Code-Hex/vz/v3 -p1 < ${./patches/vz-gui.patch}
  '';
}
