## voom list

List VMs

### Synopsis

List VMs, one per row: name, ID, status, image, CPUs, memory, SSH port, disk capacity, configured USB and PCI counts, and saved egress configuration.

A disk that cannot be read shows as disk=?. The USB, PCI, and egress-config columns show saved configuration. Use 'voom info <name>' for current USB, PCI, and egress state and host disk allocation.

Forwarding problems add forwarding=warning. Use 'voom info <name>' or 'voom forward list <name>' for details. Status shows whether the VM process is running.

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
