package vm

import (
	"context"
	"fmt"
	"time"

	"github.com/mjrusso/voom/internal/state"
)

// SetCPUs updates the configured CPU allocation for a stopped VM.
func (m *Manager) SetCPUs(ctx context.Context, name string, cpus int) (*state.VMRecord, bool, error) {
	if cpus < 1 {
		return nil, false, fmt.Errorf("cpus must be at least 1")
	}
	vm, unlock, err := m.store.LockVMRecord(ctx, name)
	if err != nil {
		return nil, false, err
	}
	defer unlock()
	if m.IsRunning(vm) {
		return nil, false, fmt.Errorf("VM %q is running; stop it before changing CPU allocation", name)
	}
	if vm.Resources.CPUs == cpus {
		return vm, false, nil
	}
	vm.Resources.CPUs = cpus
	vm.UpdatedAt = time.Now().UTC()
	if err := m.store.SaveVM(vm); err != nil {
		return nil, false, err
	}
	return vm, true, nil
}

// SetMemory updates the configured memory allocation for a stopped VM.
func (m *Manager) SetMemory(ctx context.Context, name string, memoryMiB int) (*state.VMRecord, bool, error) {
	if memoryMiB < 1 {
		return nil, false, fmt.Errorf("memory must be at least 1MiB")
	}
	vm, unlock, err := m.store.LockVMRecord(ctx, name)
	if err != nil {
		return nil, false, err
	}
	defer unlock()
	if m.IsRunning(vm) {
		return nil, false, fmt.Errorf("VM %q is running; stop it before changing memory allocation", name)
	}
	if vm.Resources.MemoryMiB == memoryMiB {
		return vm, false, nil
	}
	vm.Resources.MemoryMiB = memoryMiB
	vm.UpdatedAt = time.Now().UTC()
	if err := m.store.SaveVM(vm); err != nil {
		return nil, false, err
	}
	return vm, true, nil
}

// SetSSHPort updates the host SSH port for a stopped VM. A port of zero
// allocates a fresh port from the automatic SSH port range.
func (m *Manager) SetSSHPort(ctx context.Context, name string, port int) (*state.VMRecord, bool, error) {
	vm, unlock, err := m.store.LockVMRecord(ctx, name)
	if err != nil {
		return nil, false, err
	}
	defer unlock()
	if m.IsRunning(vm) {
		return nil, false, fmt.Errorf("VM %q is running; stop it before changing the SSH port", name)
	}
	if vm.Network.SSHPort == port {
		return vm, false, nil
	}
	err = m.store.WithGlobalVM(name, vm.ID, func() error {
		if port == 0 {
			port, err = m.store.AllocateSSHPort()
		} else {
			err = m.store.EnsureSSHPortAvailable(port)
		}
		if err != nil {
			return err
		}
		vm.Network.SSHPort = port
		vm.UpdatedAt = time.Now().UTC()
		return m.store.SaveVM(vm)
	})
	if err != nil {
		return nil, false, err
	}
	return vm, true, nil
}
