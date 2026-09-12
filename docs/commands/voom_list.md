## voom list

List VMs

### Synopsis

List VMs, one per row: name, ID, status, image, CPUs, memory, SSH port, disk capacity, and saved egress configuration. A disk that cannot be read shows as disk=?. The egress-config column shows the saved attachment (enabled, disabled, or none) and does not check the running VM; 'voom info' reports observed egress state and host disk allocation.

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
