## voom create

Create a stopped VM from an image

```
voom create <name> --image <image> [flags]
```

### Options

```
      --cpus int        CPU count (default 4)
      --driver string   driver: auto, qemu, or vfkit (default "auto")
  -h, --help            help for create
      --image string    image name
      --memory string   memory size (default "4096MiB")
      --ssh-port int    explicit host SSH port (0 = auto-allocate)
      --start           start after creation
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom](voom.md)	 - Magic-free local VMs
