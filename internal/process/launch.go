package process

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

const launchGateArgument = "--voom-process-launch-gate"

// DispatchLaunch handles the internal gate before normal argument parsing.
// A handled invocation execs the target or returns an exit code for the entry point.
func DispatchLaunch(argv []string) (handled bool, exitCode int) {
	if len(argv) < 2 || argv[1] != launchGateArgument {
		return false, 0
	}
	if len(argv) < 3 {
		return true, 127
	}
	gate := os.NewFile(3, "launch-gate")
	status := os.NewFile(4, "launch-status")
	defer func() { _ = status.Close() }()
	syscall.CloseOnExec(4)
	var release [1]byte
	_, err := io.ReadFull(gate, release[:])
	_ = gate.Close()
	if err != nil || release[0] != 1 {
		return true, 1
	}
	if err := syscall.Exec(argv[2], argv[2:], os.Environ()); err != nil {
		_, _ = fmt.Fprint(status, err)
	}
	return true, 127
}
