## voom config egress disable

Disable the explicit proxy attachment

### Synopsis

Disable the proxy attachment and close its active tunnels. Keep its socket reservation for later re-enabling. Ordinary direct network egress remains available.

```
voom config egress disable <name> [flags]
```

### Options

```
  -h, --help   help for disable
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom config egress](voom_config_egress.md)	 - Configure an explicit proxy attachment
