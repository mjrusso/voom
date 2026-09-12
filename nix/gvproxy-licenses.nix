{ go, go-licenses, gvproxySource, runCommand }:
runCommand "gvproxy-third-party-licenses" {
  nativeBuildInputs = [ go go-licenses ];
} ''
  export GOCACHE=$TMPDIR/go-cache
  export HOME=$TMPDIR
  cp -R ${gvproxySource} source
  chmod -R u+w source
  cd source
  go-licenses save ./cmd/gvproxy --save_path=$out
''
