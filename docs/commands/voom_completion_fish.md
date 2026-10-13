## voom completion fish

Generate the autocompletion script for fish

### Synopsis

Generate the autocompletion script for the fish shell.

To load completions in your current shell session:

	voom completion fish | source

To load completions for every new session, execute once:

	voom completion fish > ~/.config/fish/completions/voom.fish

You will need to start a new shell for this setup to take effect.


```
voom completion fish [flags]
```

### Options

```
  -h, --help              help for fish
      --no-descriptions   disable completion descriptions
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom completion](voom_completion.md)	 - Generate the autocompletion script for the specified shell
