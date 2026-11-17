{ applyPatches, fetchFromGitHub }:
applyPatches {
  name = "gvproxy-0.8.9-voom.8-source";
  src = fetchFromGitHub {
    owner = "containers";
    repo = "gvisor-tap-vsock";
    rev = "9cfc86f66679ef0feed0f20ba1df558fe2bef5c6";
    hash = "sha256-wWsxqMpHu+YY9LaiA8SohZSDCSJgYc/FnUkx6GzfhYw=";
  };
  patches = [
    ./patches/gvproxy-gateway-forward.patch
    ./patches/gvproxy-modification-notices.patch
    ./patches/gvproxy-udp-forward.patch
    ./patches/gvproxy-forward-outcomes.patch
    ./patches/gvproxy-blocked-subnets.patch
    ./patches/gvproxy-mutation-outcomes.patch
  ];
  postPatch = ''
    for source in $(find ${../internal/gvproxy/source} -name '*.go' -type f); do
      target="''${source#${../internal/gvproxy/source}/}"
      mkdir -p "$(dirname "$target")"
      sed '1,2d' "$source" > "$target"
    done
    install -m644 ${../internal/gvproxy/forwarder/mutation_outcomes.go} pkg/services/forwarder/mutation_outcomes.go
  '';
}
