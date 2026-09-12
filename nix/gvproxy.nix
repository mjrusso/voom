{ lib, buildGoModule, fetchFromGitHub }:
buildGoModule {
  pname = "gvproxy";
  version = "0.8.9-voom.2";
  src = fetchFromGitHub {
    owner = "containers";
    repo = "gvisor-tap-vsock";
    rev = "9cfc86f66679ef0feed0f20ba1df558fe2bef5c6";
    hash = "sha256-wWsxqMpHu+YY9LaiA8SohZSDCSJgYc/FnUkx6GzfhYw=";
  };
  patches = [ ./patches/gvproxy-gateway-forward.patch ];
  vendorHash = null;
  subPackages = [ "cmd/gvproxy" ];
  checkPhase = ''
    runHook preCheck
    GVPROXY_TEST_BINARY="$GOPATH/bin/gvproxy" go test -race ./pkg/services/gatewayforward ./pkg/virtualnetwork ./cmd/gvproxy
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
