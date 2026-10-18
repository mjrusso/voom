## voom debug macos-clone-bundle

Copy a MacOS bundle for identity testing

### Synopsis

Copy a stopped MacOS image bundle without importing or booting it. The destination must be new and its parent directory must exist. With --fresh-identity, use the packaged Apple identity helper to replace the copied machine identifier. The disk, hardware model, and auxiliary storage stay paired. Normal create, clone, and MacOS disk reset always generate a fresh identifier.

```
voom debug macos-clone-bundle <source-bundle> <destination> [flags]
```

### Options

```
      --fresh-identity   generate a new Apple machine identifier (requires an Apple Silicon Mac and the packaged identity helper)
  -h, --help             help for macos-clone-bundle
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom debug](voom_debug.md)	 - Debug voom internals
