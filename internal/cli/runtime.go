package cli

import (
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
	return &runtimeDeps{store: st, vm: vm.New(st)}, nil
}
