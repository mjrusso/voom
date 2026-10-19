## voom forward auto enable

Enable auto-forwarding

### Synopsis

Enable automatic application forwarding for a Linux guest. Automatic forwarding is not supported for MacOS guests; the SSH mapping remains available.

```
voom forward auto enable <name> [flags]
```

### Options

```
      --bind string   host bind address
  -h, --help          help for enable
      --lan           bind to 0.0.0.0
      --offset int    host port offset
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom forward auto](voom_forward_auto.md)	 - Configure auto-forwarding
