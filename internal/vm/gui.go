package vm

import (
	"context"
	"errors"
	"fmt"

	"github.com/mjrusso/voom/internal/driver/vfkit"
	"github.com/mjrusso/voom/internal/state"
)

func (m *Manager) prepareGUIApp(ctx context.Context, vm *state.VMRecord, executable string) (string, error) {
	if vm.MacOS == nil {
		return executable, nil
	}
	bundleID := "io.github.mjrusso.voom.vm." + vm.ID
	return buildGUIApp(ctx, executable, m.store.Runtime(vm).GUIApp(), vm.Name, bundleID)
}

// ShowGUI focuses the existing display without starting a VM.
func (m *Manager) ShowGUI(ctx context.Context, name string) error {
	vm, unlock, err := m.store.LockVMRecord(ctx, name)
	if err != nil {
		return err
	}
	defer unlock()
	if vm.MacOS == nil {
		return errors.New("graphical display control requires a MacOS guest")
	}
	if !m.IsRunning(vm) {
		return fmt.Errorf("VM %q is not running; start it first", name)
	}
	return vfkit.ShowDisplay(ctx, m.store.Runtime(vm).VFKitRestSock())
}
