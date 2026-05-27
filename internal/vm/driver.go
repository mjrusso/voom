package vm

import (
	"errors"
	"fmt"
	"os"

	"github.com/mjrusso/voom/internal/driver/qemu"
	"github.com/mjrusso/voom/internal/driver/vfkit"
	"github.com/mjrusso/voom/internal/share"
	"github.com/mjrusso/voom/internal/state"
)

func (m *Manager) qemuArgs(vm *state.VMRecord, shares []share.Runtime) []string {
	qshares := make([]qemu.Share, 0, len(shares))
	for _, sh := range shares {
		qshares = append(qshares, qemu.Share{Tag: sh.Tag, Sock: sh.Sock})
	}
	return qemu.Args(qemu.ArgsConfig{
		CPUs:        vm.Resources.CPUs,
		MemoryMiB:   vm.Resources.MemoryMiB,
		DiskPath:    m.store.VMDiskPath(vm),
		DiskFormat:  state.ImageFormatFromDisk(m.store.VMDiskPath(vm)),
		SeedImage:   m.store.SeedImagePath(vm),
		NetSock:     m.store.Runtime(vm).QEMUNetSock(),
		MAC:         state.GuestMAC(vm.Driver, vm.ID),
		SerialLog:   m.store.LogPath(vm, "serial"),
		MonitorSock: m.store.Runtime(vm).QEMUMonitor(),
		Pidfile:     m.store.Runtime(vm).VMPid(),
		Shares:      qshares,
	})
}

func (m *Manager) vfkitArgs(vm *state.VMRecord, im *state.ImageRecord, shares []share.Runtime) []string {
	kernelPath, initrdPath, kernelCmd := state.VFKitBootPaths(im)
	vshares := make([]vfkit.Share, 0, len(shares))
	for _, sh := range shares {
		vshares = append(vshares, vfkit.Share{MountTag: sh.Tag, SharedDir: sh.HostPath, Readonly: sh.Readonly})
	}
	return vfkit.Args(vfkit.ArgsConfig{
		CPUs:       vm.Resources.CPUs,
		MemoryMiB:  vm.Resources.MemoryMiB,
		KernelPath: kernelPath,
		InitrdPath: initrdPath,
		KernelCmd:  kernelCmd,
		BootDisk:   m.store.VMDiskPath(vm),
		SeedImage:  m.store.SeedImagePath(vm),
		NetSock:    m.store.Runtime(vm).VFKitNetSock(),
		MAC:        state.GuestMAC(vm.Driver, vm.ID),
		SerialLog:  m.store.LogPath(vm, "serial"),
		RestSock:   m.store.Runtime(vm).VFKitRestSock(),
		EFIStore:   m.store.Runtime(vm).EFIStore(),
		Shares:     vshares,
	})
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
