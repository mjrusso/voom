{ go, go-licenses, vfkitSource, runCommand, apple-sdk_15 }:
runCommand "vfkit-third-party-licenses" {
  nativeBuildInputs = [ go go-licenses ];
  buildInputs = [ apple-sdk_15 ];
} ''
  export GOCACHE=$TMPDIR/go-cache
  cp -R ${vfkitSource} source
  chmod -R u+w source
  cd source
  go-licenses save ./cmd/vfkit --save_path=$out
''
