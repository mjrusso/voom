package vm

import (
	"context"
	"errors"
	"os"

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

func (m *Manager) validateGuestImage(vm *state.VMRecord, im *state.ImageRecord) error {
	if err := validateImageControl(vm, im); err != nil {
		return err
	}

	if vm.MacOS == nil && vm.Driver == "vfkit" && im.Metadata.KernelPath == "" {
		if err := image.ValidateVFKitBootDisk(m.store.VMDiskPath(vm)); err != nil {
			return err
		}
	}
	if vm.Driver == "qemu" && ((im.Capabilities.ControlShare && !im.Capabilities.VsockControl) || len(vm.Shares) > 0) {
		if err := host.RequireExe("virtiofsd"); err != nil {
			return err
		}
	}
	return validateImageFeatures(vm, im)
}

func (m *Manager) prepareGuestFiles(vm *state.VMRecord, im *state.ImageRecord) error {
	if vm.MacOS == nil {
		if err := m.WriteSeedImage(vm); err != nil {
			return err
		}
	}
	if im.Capabilities.ControlShare || im.Capabilities.VsockControl {
		if err := m.WriteControlFiles(vm, im.Capabilities.VsockControl); err != nil {
			return err
		}
		if vm.MacOS != nil || im.Capabilities.VsockControl {
			if err := os.Remove(m.store.ControlSharePortsPath(vm)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

// Watchers require a recorded driver process and its ready control socket.
func (m *Manager) startGuestWatchers(vm *state.VMRecord, im *state.ImageRecord) error {
	if vm.Network.AutoForward && im.Capabilities.GuestPortReport && (im.Capabilities.ControlShare || im.Capabilities.VsockControl) {
		if err := m.StartAutoForwardWatcher(vm); err != nil {
			return err
		}
	}
	if vm.MacOS != nil || im.Capabilities.VsockControl {
		if err := m.startRuntimeWatcher(vm); err != nil {
			return err
		}
	}
	return nil
}
