// Command voom is the CLI for managing local development VMs on Linux (QEMU/KVM) and MacOS (vfkit).
package main

import (
	"os"

	"github.com/mjrusso/voom/internal/cli"
	"github.com/mjrusso/voom/internal/process"
)

func main() {
	if handled, code := process.DispatchLaunch(os.Args); handled {
		os.Exit(code)
	}
	cli.Execute()
}
