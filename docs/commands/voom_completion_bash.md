## voom completion bash

Generate the autocompletion script for bash

### Synopsis

Generate the autocompletion script for the bash shell.

This script depends on the 'bash-completion' package.
If it is not installed already, you can install it via your OS's package manager.

To load completions in your current shell session:

	source <(voom completion bash)

To load completions for every new session, execute once:

#### Linux:

	voom completion bash > /etc/bash_completion.d/voom

#### macOS:

	voom completion bash > $(brew --prefix)/etc/bash_completion.d/voom

You will need to start a new shell for this setup to take effect.


```
voom completion bash
```

### Options

```
  -h, --help              help for bash
      --no-descriptions   disable completion descriptions
```

### Options inherited from parent commands

```
      --output string   output format: text or json (default "text")
  -v, --verbose         enable verbose diagnostics
```

### SEE ALSO

* [voom completion](voom_completion.md)	 - Generate the autocompletion script for the specified shell
