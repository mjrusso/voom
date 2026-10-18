{ lib, buildGoModule, apple-sdk_15, darwin, vfkitSource, vfkitLicenses }:
buildGoModule {
  pname = "vfkit";
  version = "0.6.4-voom.1";
  src = vfkitSource;
  vendorHash = null;
  subPackages = [ "cmd/vfkit" ];
  nativeBuildInputs = [ darwin.sigtool ];
  buildInputs = [ apple-sdk_15 ];
  env.CGO_ENABLED = 1;
  ldflags = [ "-s" "-w" ];
  postFixup = ''
    codesign --entitlements vf.entitlements --force --sign - $out/bin/vfkit
  '';
  doInstallCheck = true;
  installCheckPhase = ''
    runHook preInstallCheck
    $out/bin/vfkit --capabilities | grep -q '"gui-lifecycle-v1":1'
    $out/bin/vfkit --capabilities | grep -q '"virtiofs-readonly-v1":1'
    runHook postInstallCheck
  '';
  checkPhase = ''
    runHook preCheck
    go test ./pkg/cmdline ./pkg/rest ./pkg/util
    go test ./pkg/config -run 'TestVirtioFsReadOnlyRoundTrip|TestJSON'
    runHook postCheck
  '';
  postInstall = ''
    mkdir -p $out/share/licenses/vfkit
    install -Dm644 LICENSE $out/share/licenses/vfkit/LICENSE.vfkit
    install -Dm644 ${./NOTICE.vfkit} $out/share/licenses/vfkit/NOTICE.vfkit
    ln -s ${vfkitLicenses} $out/share/licenses/vfkit/THIRD_PARTY_LICENSES
  '';
  meta = {
    description = "vfkit with persistent GUI windows and display control";
    homepage = "https://github.com/crc-org/vfkit";
    license = lib.licenses.asl20;
    platforms = lib.platforms.darwin;
    mainProgram = "vfkit";
  };
}
