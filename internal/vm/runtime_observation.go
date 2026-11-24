package vm

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

// RuntimeState distinguishes confirmed driver states from failed inspections.
type RuntimeState string

// States returned by ObserveRuntime.
const (
	RuntimeUnknown RuntimeState = "unknown"
	RuntimeRunning RuntimeState = "running"
	RuntimeStopped RuntimeState = "stopped"
)

// ObserveRuntime preserves inspection failures as RuntimeUnknown. RuntimeStopped
// requires confirmed exit or absence of both the record and driver artifacts.
// Callers making stopped-only changes must hold the VM lock across observation and mutation.
func (m *Manager) ObserveRuntime(vm *state.VMRecord) (RuntimeState, error) {
	return m.ObserveRuntimeContext(context.Background(), vm)
}

// ObserveRuntimeContext applies ctx to runtime inspection and preserves cancellation as RuntimeUnknown.
func (m *Manager) ObserveRuntimeContext(ctx context.Context, vm *state.VMRecord) (RuntimeState, error) {
	status, err := m.observeCurrentRuntime(ctx, vm)
	if status == RuntimeStopped {
		if err := state.CheckPreviousVMRuntime(m.previousRuntime, vm); err != nil {
			return RuntimeUnknown, err
		}
	}
	return status, err
}

func (m *Manager) observeCurrentRuntime(ctx context.Context, vm *state.VMRecord) (RuntimeState, error) {
	rt := m.store.Runtime(vm)
	path := rt.VMProcessRecord()
	observed, err := process.ObserveRecordContext(ctx, path, state.VMProcessKind(vm.Driver), "")
	if errors.Is(err, os.ErrNotExist) {
		if _, statErr := os.Lstat(path); errors.Is(statErr, os.ErrNotExist) {
			for _, artifact := range rt.DriverArtifacts(vm.Driver) {
				_, artifactErr := os.Lstat(artifact)
				if errors.Is(artifactErr, os.ErrNotExist) {
					continue
				}
				if artifactErr == nil {
					artifactErr = fmt.Errorf("driver artifact %s remains without process identity", artifact)
				}
				return RuntimeUnknown, fmt.Errorf("cannot confirm runtime state of VM %q: %w", vm.Name, artifactErr)
			}
			return RuntimeStopped, nil
		}
	}
	if err != nil {
		return RuntimeUnknown, fmt.Errorf("cannot confirm runtime state of VM %q: %w", vm.Name, err)
	}
	switch observed.State {
	case process.RecordRunning:
		return RuntimeRunning, nil
	case process.RecordExited:
		return RuntimeStopped, nil
	default:
		return RuntimeUnknown, fmt.Errorf("cannot confirm runtime state of VM %q", vm.Name)
	}
}

func (m *Manager) requireStopped(vm *state.VMRecord, action string) error {
	observed, err := m.ObserveRuntime(vm)
	if err != nil {
		return err
	}
	if observed != RuntimeStopped {
		return fmt.Errorf("VM %q is running; stop it before %s", vm.Name, action)
	}
	return nil
}
