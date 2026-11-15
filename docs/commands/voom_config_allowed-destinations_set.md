## voom config allowed-destinations set

Replace global exceptions to the tailnet block

### Synopsis

Replace the IP:PORT exceptions to the guest tailnet block for all VMs. Each exception allows TCP and UDP. Use literal IP addresses and ports 1-65535; bracket IPv6 addresses. Changes take effect when each VM next starts.

```
voom config allowed-destinations set <IP:PORT...> [flags]
```

### Options

```
  -h, --help   help for set
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom config allowed-destinations](voom_config_allowed-destinations.md)	 - Show global exceptions to the tailnet block
