package vm

import (
	"context"

	"github.com/mjrusso/voom/internal/host"
	"github.com/mjrusso/voom/internal/image"
	"github.com/mjrusso/voom/internal/state"
)

// The caller must confirm runtime termination before recovering persistent storage.
func (m *Manager) prepareGuestStorage(ctx context.Context, vm *state.VMRecord) error {
	if err := host.ValidateHostImage(vm.Arch, state.ImageFormatFromDisk(m.store.VMDiskPath(vm)), vm.Driver); err != nil {
		return err
	}
	if vm.MacOS != nil {
		if err := state.WaitMacOSStorage(ctx, m.store.VMStorageDir(vm)); err != nil {
			return err
		}
	}
	return m.store.CleanupStorage(vm)
}

func (m *Manager) validateGuestDriver(vm *state.VMRecord, im *state.ImageRecord) error {
	if vm.MacOS == nil && vm.Driver == "vfkit" && im.Metadata.KernelPath == "" {
		if err := image.ValidateVFKitBootDisk(m.store.VMDiskPath(vm)); err != nil {
			return err
		}
	}
	if vm.Driver == "qemu" && (im.Capabilities.ControlShare || len(vm.Shares) > 0) {
		if err := host.RequireExe("virtiofsd"); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) prepareGuestFiles(vm *state.VMRecord, im *state.ImageRecord) error {
	if vm.MacOS != nil {
		return nil
	}
	if err := m.WriteSeedImage(vm); err != nil {
		return err
	}
	if im.Capabilities.ControlShare {
		if err := m.WriteControlFiles(vm); err != nil {
			return err
		}
	}
	return nil
}

// Watchers require a recorded driver process and its ready control socket.
func (m *Manager) startGuestWatchers(vm *state.VMRecord, im *state.ImageRecord) error {
	if vm.Network.AutoForward && im.Capabilities.GuestPortReport && im.Capabilities.ControlShare {
		if err := m.StartAutoForwardWatcher(vm); err != nil {
			return err
		}
	}
	if vm.MacOS != nil {
		if err := m.startRuntimeWatcher(vm); err != nil {
			return err
		}
	}
	return nil
}
