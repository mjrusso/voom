## voom list

List VMs

### Synopsis

List VMs, one per row: name, ID, status, image, CPUs, memory, SSH port, and disk capacity. A disk that cannot be read shows as disk=?. 'voom info' also reports host disk allocation.

```
voom list [flags]
```

### Options

```
  -h, --help   help for list
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom](voom.md)	 - Magic-free local VMs
