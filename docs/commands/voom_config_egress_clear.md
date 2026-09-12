## voom config egress clear

Clear the explicit proxy attachment

### Synopsis

Remove a stopped VM's proxy attachment and release its backend socket reservation. Ordinary direct network egress remains available.

```
voom config egress clear <name> [flags]
```

### Options

```
  -h, --help   help for clear
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom config egress](voom_config_egress.md)	 - Configure an explicit proxy attachment
