## voom forward reconcile

Apply pending forwarding changes without restarting the VM

### Synopsis

Confirm pending requests and apply saved manual and automatic forwards. If a request is still unconfirmed after this attempt, the command returns an error. Voom keeps its ports reserved and continues checking in the background.

```
voom forward reconcile <name> [flags]
```

### Options

```
  -h, --help               help for reconcile
      --timeout duration   time limit for this attempt (default 10s)
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom forward](voom_forward.md)	 - Manage guest port forwards
