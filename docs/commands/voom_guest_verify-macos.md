## voom guest verify-macos

Collect macOS guest identity, boot, SSH, and Nix-daemon evidence

### Synopsis

Run read-only diagnostic commands over SSH against a running macOS guest. Retain stdout/stderr and check failures in JSON output. Does not start, stop, initialize, or change the guest's identity. A passing report does not establish clone/reset safety; compare reports from independent copies and repeated boots.

```
voom guest verify-macos <name> [flags]
```

### Options

```
  -h, --help               help for verify-macos
      --timeout duration   total SSH diagnostic timeout (default 2m0s)
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom guest](voom_guest.md)	 - Inspect guest-reported state
