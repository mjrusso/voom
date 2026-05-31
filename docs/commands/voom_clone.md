## voom clone

Clone a stopped VM into a new VM

### Synopsis

Create a new VM by copying a stopped VM's current disk. The clone gets a fresh ID and a newly-allocated SSH port and keeps the source's resources and access. It does not inherit the source's shares, manual forwards, or auto-forward settings, but prints the commands to reproduce them on the clone.

```
voom clone <source-name> <new-name> [flags]
```

### Options

```
  -h, --help   help for clone
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom](voom.md)	 - Magic-free local VMs
