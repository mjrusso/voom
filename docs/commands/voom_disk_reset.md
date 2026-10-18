## voom disk reset

Reset VM disk from image

### Synopsis

Stop the VM and replace its guest state from an image. This discards guest changes. For macOS, replace the disk and Apple platform files together and generate a fresh Apple identity. Keep the VM name, Voom ID, CPU and memory allocation, SSH port, NIC MAC, shares, and forwarding settings. Reset cannot change between Linux and macOS.

```
voom disk reset <name> --image <image> [flags]
```

### Options

```
  -h, --help           help for reset
      --image string   image name
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom disk](voom_disk.md)	 - Manage VM disks
