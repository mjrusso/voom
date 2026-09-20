## voom list

List VMs

### Synopsis

List VMs, one per row: name, ID, status, image, CPUs, memory, SSH port, disk capacity, configured USB count, and saved egress configuration. A disk that cannot be read shows as disk=?. The USB count and egress-config columns show saved configuration; 'voom info' reports observed USB and egress state and host disk allocation.

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
