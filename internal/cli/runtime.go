package cli

import (
	"fmt"
	"os"

	"github.com/mjrusso/voom/internal/events"
	"github.com/mjrusso/voom/internal/state"
	"github.com/mjrusso/voom/internal/vm"
)

type runtimeDeps struct {
	store *state.Store
	vm    *vm.Manager
}

var openStore = func() (*state.Store, error) {
	return state.Open()
}

func loadRuntimeDeps() (*runtimeDeps, error) {
	st, err := openStore()
	if err != nil {
		return nil, err
	}
	var warn func(string)
	if verboseRequested(os.Args[1:]) {
		warn = func(message string) {
			_, _ = fmt.Fprintf(os.Stderr, "event emission failed: %s\n", message)
		}
	}
	emitter := events.NewEmitter(st.Paths().Cache, warn)
	return &runtimeDeps{store: st, vm: vm.New(st, vm.WithEventEmitter(emitter))}, nil
}

func verboseRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--verbose" || arg == "-v" {
			return true
		}
	}
	return false
}
