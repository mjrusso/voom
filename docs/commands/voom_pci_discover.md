## voom pci discover

List host PCI Express slots available for passthrough

### Synopsis

List the PCI slots on this Linux host that contain something other than bridges. Each row contains the slot address, each function's vendor:device ID and driver, and whether the slot can be assigned or why not.

```
voom pci discover [flags]
```

### Options

```
  -h, --help   help for discover
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom pci](voom_pci.md)	 - Manage Linux PCI passthrough, such as GPUs
