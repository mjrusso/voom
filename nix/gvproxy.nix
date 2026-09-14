{ lib, buildGoModule, gvproxyLicenses, gvproxyNotice, gvproxySource }:
buildGoModule {
  pname = "gvproxy";
  version = "0.8.9-voom.3";
  src = gvproxySource;
  vendorHash = null;
  subPackages = [ "cmd/gvproxy" ];
  postInstall = ''
    mkdir -p $out/share/licenses/gvproxy
    ln -s ${gvproxyLicenses} $out/share/licenses/gvproxy/THIRD_PARTY_LICENSES
    install -Dm644 LICENSE $out/share/licenses/gvproxy/LICENSE.gvproxy
    install -Dm644 ${gvproxyNotice} $out/share/licenses/gvproxy/NOTICE.gvproxy
  '';
  checkPhase = ''
    runHook preCheck
    GVPROXY_TEST_BINARY="$GOPATH/bin/gvproxy" go test -race ./pkg/services/forwarder ./pkg/services/gatewayforward ./pkg/virtualnetwork ./cmd/gvproxy
    runHook postCheck
  '';
  meta = {
    description = "gvproxy with guest isolation and private gateway TCP-to-Unix transport";
    homepage = "https://github.com/containers/gvisor-tap-vsock";
    license = lib.licenses.asl20;
    platforms = lib.platforms.unix;
    mainProgram = "gvproxy";
  };
}
