## voom debug macos-clone-bundle

Prepare a disposable macOS bundle for identity experiments

### Synopsis

Copy a stopped builder bundle into a new directory whose parent already exists. Does not import or boot the copy. With --fresh-identity, use the packaged native helper to generate and read back a new Apple machine identifier, preserving the copied disk, hardware model, and auxiliary storage. Normal create and clone also assign fresh identities; reset remains unsupported.

```
voom debug macos-clone-bundle <source-bundle> <destination> [flags]
```

### Options

```
      --fresh-identity   generate a new Apple machine identifier (requires an Apple Silicon Mac and Swift)
  -h, --help             help for macos-clone-bundle
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom debug](voom_debug.md)	 - Debug voom internals
