## voom config show

Print the commands to reproduce a VM's configuration

### Synopsis

Print the shell commands that recreate a VM's post-create configuration: its shares, manual forwards, and auto-forward settings. The output is empty for a VM with none of these. ('voom clone' prints the same commands, retargeted at the new VM, so you can match a clone to its source.)

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

* [voom config](voom_config.md)	 - Inspect VM configuration
