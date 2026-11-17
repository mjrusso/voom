## voom info

Inspect a VM

### Synopsis

Show saved VM configuration, disk usage, and current USB, PCI, egress, and forwarding status.

Forwarding problems appear as warnings. Status shows whether the VM process is running. Use 'voom forward list <name>' to inspect each forward.

```
voom info <name> [flags]
```

### Options

```
  -h, --help   help for info
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom](voom.md)	 - Magic-free local VMs
