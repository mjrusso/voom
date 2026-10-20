package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/mjrusso/voom/internal/driver/vfkit"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

func (m *Manager) prepareGUIApp(ctx context.Context, vm *state.VMRecord, executable string) (string, error) {
	if vm.MacOS == nil {
		return executable, nil
	}
	appName := "Voom: " + vm.Name
	bundleID := "io.github.mjrusso.voom.vm." + vm.ID
	return buildGUIApp(ctx, executable, m.store.Runtime(vm).GUIApp(), appName, bundleID)
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

func (m *Manager) startRuntimeWatcher(vm *state.VMRecord) error {
	rt := m.store.Runtime(vm)
	fingerprint, err := process.RecordFingerprint(rt.VMProcessRecord())
	if err != nil {
		return err
	}
	return m.startSelfRecorded([]string{"debug", "watch-vm", vm.ID, fingerprint}, m.store.LogPath(vm, "runtime-watch"), rt.RuntimeWatcherProcessRecord())
}

func (m *Manager) stopRuntimeWatcher(vm *state.VMRecord) error {
	path := m.store.Runtime(vm).RuntimeWatcherProcessRecord()
	if pid, ok := process.ValidRecord(path, "runtime-watch"); ok && pid == os.Getpid() {
		return nil
	}
	return process.StopRecorded(path, "runtime-watch", processStopTimeout)
}

// WatchRuntime removes helpers only after confirming that the recorded driver exited.
func (m *Manager) WatchRuntime(ctx context.Context, id, fingerprint string) error {
	vm, err := m.store.LoadVMByID(id)
	if err != nil {
		return err
	}
	rt := m.store.Runtime(vm)
	defer func() {
		// A newer start may have installed a replacement watcher at this path.
		if pid, ok := process.ValidRecord(rt.RuntimeWatcherProcessRecord(), "runtime-watch"); ok && pid == os.Getpid() {
			_ = process.RemoveRecord(rt.RuntimeWatcherProcessRecord())
		}
	}()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		observed, err := process.ObserveRecord(rt.VMProcessRecord(), state.VMProcessKind(vm.Driver), fingerprint)
		if err != nil {
			return err
		}
		if observed.State == process.RecordChanged {
			return nil
		}
		if observed.State == process.RecordExited {
			unlock, err := m.store.TryLockVM(id)
			if err != nil {
				return err
			}
			if unlock != nil {
				err := m.cleanupExitedRuntime(ctx, id, fingerprint)
				unlock()
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *Manager) cleanupExitedRuntime(ctx context.Context, id, fingerprint string) error {
	vm, err := m.store.LoadVMByID(id)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := m.store.WithGlobalVM(vm.Name, id, func() error { return nil }); err != nil {
		return err
	}
	observed, err := process.ObserveRecord(m.store.Runtime(vm).VMProcessRecord(), state.VMProcessKind(vm.Driver), fingerprint)
	if err != nil {
		return err
	}
	if observed.State != process.RecordExited {
		return nil
	}
	_, err = m.stopLocked(ctx, vm)
	return err
}
