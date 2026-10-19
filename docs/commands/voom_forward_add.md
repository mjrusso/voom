## voom forward add

Add a manual TCP or UDP forward

### Synopsis

Add an application port forward to a Linux guest. Application forwarding is not supported for MacOS guests; the SSH mapping remains available.

```
voom forward add <name> <guest-port> [flags]
```

### Examples

```
  voom forward add dev 8080 --protocol tcp --host-port 18080 --bind 127.0.0.1
  voom forward add dev 5353 --protocol udp --host-port 15353 --bind 127.0.0.1
```

### Options

```
      --auto              allocate a free host port
      --bind string       host bind address
  -h, --help              help for add
      --host-port int     host port
      --lan               bind to 0.0.0.0
      --protocol string   transport protocol (tcp or udp) (default "tcp")
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom forward](voom_forward.md)	 - Manage guest port forwards
