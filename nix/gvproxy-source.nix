{ applyPatches, fetchFromGitHub }:
applyPatches {
  name = "gvproxy-0.8.9-voom.4-source";
  src = fetchFromGitHub {
    owner = "containers";
    repo = "gvisor-tap-vsock";
    rev = "9cfc86f66679ef0feed0f20ba1df558fe2bef5c6";
    hash = "sha256-wWsxqMpHu+YY9LaiA8SohZSDCSJgYc/FnUkx6GzfhYw=";
  };
  patches = [
    ./patches/gvproxy-gateway-forward.patch
    ./patches/gvproxy-modification-notices.patch
  ];
}
