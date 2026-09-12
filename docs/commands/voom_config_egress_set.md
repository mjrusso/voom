## voom config egress set

Set the explicit proxy attachment

### Synopsis

Attach a unique host Unix proxy socket to a stopped VM and enable the attachment. An optional CA file must contain public certificates only. Ordinary direct network egress remains available.

```
voom config egress set <name> [flags]
```

### Options

```
      --backend-socket string   absolute path to this VM's host Unix proxy socket
      --ca-cert string          public CA certificate PEM file
  -h, --help                    help for set
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom config egress](voom_config_egress.md)	 - Configure an explicit proxy attachment
