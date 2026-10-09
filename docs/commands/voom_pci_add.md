## voom pci add

Assign a host PCI Express slot or function to a VM

### Synopsis

Assign a host PCI Express slot, such as 0000:41:00, or one of its functions, such as 0000:41:00.0, to a stopped QEMU VM. A slot assigns all of its functions. The assigned functions must be bound to vfio-pci, and every other device in their IOMMU groups must be bound to vfio-pci or have no driver. Voom records each function's vendor:device ID and refuses to start the VM if the hardware at that address changes.

```
voom pci add <name> <device-name> <slot-or-function> [flags]
```

### Options

```
  -h, --help   help for add
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom pci](voom_pci.md)	 - Manage Linux PCI passthrough, such as GPUs
