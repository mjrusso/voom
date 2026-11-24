package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mjrusso/voom/internal/driver/qemu"
	"github.com/mjrusso/voom/internal/driver/vfkit"
	"github.com/mjrusso/voom/internal/host"
	"github.com/mjrusso/voom/internal/share"
	"github.com/mjrusso/voom/internal/state"
	"github.com/mjrusso/voom/internal/usb"
)

func (m *Manager) qemuArgs(vm *state.VMRecord, firmwarePath string, shares []share.Runtime, usbBindings []usb.Binding) ([]string, error) {
	qshares := make([]qemu.Share, 0, len(shares))
	for _, sh := range shares {
		qshares = append(qshares, qemu.Share{Tag: sh.Tag, Sock: sh.Sock})
	}
	return qemu.Args(qemu.ArgsConfig{
		Arch:         vm.Arch,
		FirmwarePath: firmwarePath,
		CPUs:         vm.Resources.CPUs,
		MemoryMiB:    vm.Resources.MemoryMiB,
		DiskPath:     m.store.VMDiskPath(vm),
		DiskFormat:   state.ImageFormatFromDisk(m.store.VMDiskPath(vm)),
		SeedImage:    m.store.SeedImagePath(vm),
		NetSock:      m.store.Runtime(vm).QEMUNetSock(),
		MAC:          state.GuestMAC(vm.Driver, vm.ID),
		SerialLog:    m.store.LogPath(vm, "serial"),
		MonitorSock:  m.store.Runtime(vm).QEMUMonitor(),
		Shares:       qshares,
		USBDevices:   usbBindings,
		PCIDevices:   vm.PCIDevices,
	})
}

func (m *Manager) vfkitArgs(vm *state.VMRecord, im *state.ImageRecord, shares []share.Runtime) ([]string, error) {
	vshares := make([]vfkit.Share, 0, len(shares))
	for _, sh := range shares {
		vshares = append(vshares, vfkit.Share{MountTag: sh.Tag, SharedDir: sh.HostPath, Readonly: sh.Readonly})
	}
	config := vfkit.ArgsConfig{
		CPUs:      vm.Resources.CPUs,
		MemoryMiB: vm.Resources.MemoryMiB,
		BootDisk:  m.store.VMDiskPath(vm),
		NetSock:   m.store.Runtime(vm).VFKitNetSock(),
		MAC:       state.GuestMAC(vm.Driver, vm.ID),
		RestSock:  m.store.Runtime(vm).VFKitRestSock(),
		Shares:    vshares,
	}
	if vm.MacOS != nil {
		dir := m.store.VMStorageDir(vm)
		config.MacOS = &vfkit.MacOSBoot{
			WindowTitle:       vm.Name,
			ApplicationName:   "Voom: " + vm.Name,
			MachineIdentifier: filepath.Join(dir, "MachineIdentifier"),
			HardwareModel:     filepath.Join(dir, "HardwareModel"),
			AuxiliaryStorage:  filepath.Join(dir, "AuxiliaryStorage"),
		}
	} else {
		kernelPath, initrdPath, kernelCmd := state.VFKitBootPaths(im)
		config.Linux = &vfkit.LinuxBoot{
			KernelPath: kernelPath, InitrdPath: initrdPath, KernelCmd: kernelCmd,
			SeedImage: m.store.SeedImagePath(vm), SerialLog: m.store.LogPath(vm, "serial"),
		}
		if kernelPath == "" {
			path, create, err := m.vfkitEFIStore(vm)
			if err != nil {
				return nil, err
			}
			config.Linux.EFIStore, config.Linux.CreateEFIStore = path, create
		}
	}
	return vfkit.Args(config)
}

func (m *Manager) vfkitEFIStore(vm *state.VMRecord) (path string, create bool, err error) {
	dst := m.store.VMEFIStorePath(vm)
	info, err := os.Lstat(dst)
	if err == nil {
		if !info.Mode().IsRegular() {
			return "", false, fmt.Errorf("EFI variable store %s must be a regular file", dst)
		}
		return dst, false, nil
	}
	if !os.IsNotExist(err) {
		return "", false, err
	}
	for _, root := range []string{m.store.Paths().Runtime, m.previousRuntime} {
		src, err := state.RuntimeEFIStore(root, vm)
		if err != nil {
			return "", false, err
		}
		if src != "" {
			return "", false, fmt.Errorf("copy the existing EFI variable store from %q to %q before starting VM %q", src, dst, vm.Name)
		}
	}
	return dst, true, nil
}

// ResolveAarch64UEFI locates a usable aarch64 UEFI firmware blob, honoring the
// VOOM_QEMU_AARCH64_UEFI environment variable before checking known paths.
func ResolveAarch64UEFI() (string, error) {
	if uefi := os.Getenv("VOOM_QEMU_AARCH64_UEFI"); uefi != "" {
		if !fileExists(uefi) {
			return "", fmt.Errorf("aarch64 UEFI firmware not found at %s", uefi)
		}
		return uefi, nil
	}
	for _, p := range []string{"/usr/share/AAVMF/AAVMF_CODE.fd", "/usr/share/qemu/edk2-aarch64-code.fd", "/usr/share/edk2/aarch64/QEMU_EFI.fd"} {
		if fileExists(p) {
			return p, nil
		}
	}
	return "", errors.New("aarch64 UEFI firmware not found; set VOOM_QEMU_AARCH64_UEFI")
}

func requireDriver(ctx context.Context, vm *state.VMRecord) (string, error) {
	switch vm.Driver {
	case "qemu":
		executable, err := host.ExePath(qemu.ExecutableName(vm.Arch))
		if err == nil && len(vm.USBDevices) > 0 {
			err = qemu.RequireUSB(vm.Arch)
		}
		if err == nil && len(vm.PCIDevices) > 0 {
			err = qemu.RequireVFIO(vm.Arch)
		}
		if err != nil {
			return "", err
		}
		if err := host.RequireExe("qemu-img"); err != nil {
			return "", err
		}
		return executable, nil
	case "vfkit":
		var capabilities []string
		if vm.MacOS != nil {
			capabilities = append(capabilities, "gui-lifecycle-v1", vfkit.GUIBrandingCapability)
		}
		for _, sh := range vm.Shares {
			if sh.Readonly {
				capabilities = append(capabilities, "virtiofs-readonly-v1")
				break
			}
		}
		return vfkit.ResolveCompatible(ctx, capabilities...)
	default:
		return "", fmt.Errorf("unsupported driver %q", vm.Driver)
	}
}
