## voom image import

Import an image

```
voom image import <name> <path> [flags]
```

### Options

```
      --arch string             image architecture/system
      --format string           image format: qcow2 or raw
  -h, --help                    help for import
      --install-guest-helpers   install voom guest helpers via cloud-init (enables shares + auto-discover)
      --meta string             metadata sidecar path
      --ssh-user string         SSH login user (overrides sidecar user)
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom image](voom_image.md)	 - Manage images
