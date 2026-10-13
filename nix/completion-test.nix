{ runCommand, voom, bash, fish, zsh }:

runCommand "voom-shell-completions" {
  nativeBuildInputs = [ voom bash fish zsh ];
} ''
  bash -n ${voom}/share/bash-completion/completions/voom.bash
  fish -n ${voom}/share/fish/vendor_completions.d/voom.fish
  zsh -n ${voom}/share/zsh/site-functions/_voom

  export NIX_PROFILES="${voom}"
  export XDG_CONFIG_HOME="$TMPDIR/config"
  export XDG_CACHE_HOME="$TMPDIR/cache"
  export XDG_DATA_HOME="$TMPDIR/data"
  export VOOM_STATE_DIR="$TMPDIR/state"
  mkdir -p "$VOOM_STATE_DIR"
  cat > "$VOOM_STATE_DIR/state.json" <<'JSON'
  {"schemaVersion":1,"vms":{"completion-vm":"vm-id"},"images":{"completion-image":"image-id"}}
  JSON

  fish -c 'complete -C "voom stop completion-"' > vm-completions
  grep -Fx completion-vm vm-completions
  fish -c 'complete -C "voom create fresh --image completion-"' > image-completions
  grep -Fx completion-image image-completions
  touch "$out"
''
