## voom guest verify

Check guest health and configured services

### Synopsis

Check a running Linux or MacOS guest over SSH. Verify guest health, mounts, and the services enabled by the image and VM configuration. Check Nix when detected; images with nixosSwitch require it. JSON output includes each check's output and errors. The command runs once without waiting for boot or changing the guest. Test restart, clone, reset, GUI, and forwarded traffic separately.

```
voom guest verify <name> [flags]
```

### Options

```
  -h, --help               help for verify
      --timeout duration   total diagnostic timeout (no boot retries) (default 2m0s)
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom guest](voom_guest.md)	 - Inspect guest-reported state
