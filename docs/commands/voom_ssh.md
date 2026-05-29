## voom ssh

SSH into a VM, or run a one-shot command

### Synopsis

Open an interactive SSH session to a running VM. Any trailing arguments are passed through to ssh as a remote command, so `voom ssh <name> -- uname -a` runs `uname -a` in the VM and exits. The `--` separator is optional but recommended when the remote command itself takes flags.

```
voom ssh <name> [--] [command...] [flags]
```

### Options

```
  -h, --help   help for ssh
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom](voom.md)	 - Magic-free local VMs
