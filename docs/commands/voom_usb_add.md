## voom usb add

Assign a host USB topology route to a VM

### Synopsis

Assign a stable USB topology location reported by 'voom usb discover', such as usb-0000:00:14.0@2-3.2, to a QEMU VM. A running VM receives the device immediately. Any device occupying that route while the VM runs is exposed to the guest.

```
voom usb add <vm> <name> <location> [flags]
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

* [voom usb](voom_usb.md)	 - Manage Linux USB passthrough
