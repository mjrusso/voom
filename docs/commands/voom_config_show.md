## voom config show

Print the commands to reproduce a VM's configuration

### Synopsis

Print the shell commands that recreate a VM's post-create configuration: its shares, USB assignments, manual forwards, auto-forward settings, and explicit egress attachment. The output is empty for a VM with none of these. ('voom clone' retargets commands, but omits egress because the clone needs its own backend socket.)

```
voom config show <name> [flags]
```

### Options

```
  -h, --help   help for show
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom config](voom_config.md)	 - Inspect and modify VM configuration
