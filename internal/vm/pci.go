package vm

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"time"

	"golang.org/x/sys/unix"

	"github.com/mjrusso/voom/internal/pci"
	"github.com/mjrusso/voom/internal/state"
)

// memlockMargin is added to guest memory to get the RLIMIT_MEMLOCK that VFIO
// needs, matching libvirt's x86 VFIO calculation.
const memlockMargin = 1 << 30

// PCIState describes a configured PCI assignment on the host.
type PCIState string

// States returned in PCIStatus.
const (
	PCIReady       PCIState = "ready"
	PCIActive      PCIState = "active"
	PCIUnavailable PCIState = "unavailable"
)

// PCIStatus reports whether a configured PCI assignment can be used.
type PCIStatus struct {
	Name     string   `json:"name"`
	Selector string   `json:"selector"`
	State    PCIState `json:"state"`
	Error    string   `json:"error,omitempty"`
}

// AddPCIDevice assigns a host PCI slot, or one function of it, to a stopped
// QEMU VM.
func (m *Manager) AddPCIDevice(ctx context.Context, vmName, deviceName, selector string) (pci.Decl, error) {
	sel, err := pci.ParseSelector(selector)
	if err != nil {
		return pci.Decl{}, err
	}
	vm, unlock, err := m.store.LockVMRecord(ctx, vmName)
	if err != nil {
		return pci.Decl{}, err
	}
	defer unlock()
	if err := pciSupported(vm); err != nil {
		return pci.Decl{}, err
	}
	if m.IsRunning(vm) {
		return pci.Decl{}, fmt.Errorf("VM %q is running; stop it before changing PCI assignments", vm.Name)
	}
	inspection := pci.Inspect(sel)
	if inspection.Err != nil {
		return pci.Decl{}, inspection.Err
	}
	decl := pci.Decl{Name: deviceName, Slot: sel.Slot, Functions: inspection.Functions}
	vm.PCIDevices = append(vm.PCIDevices, decl)
	vm.UpdatedAt = time.Now().UTC()
	return decl, m.store.SaveVM(vm)
}

// RemovePCIDevice removes a PCI assignment from a stopped VM.
func (m *Manager) RemovePCIDevice(ctx context.Context, vmName, deviceName string) error {
	vm, unlock, err := m.store.LockVMRecord(ctx, vmName)
	if err != nil {
		return err
	}
	defer unlock()
	if m.IsRunning(vm) {
		return fmt.Errorf("VM %q is running; stop it before changing PCI assignments", vm.Name)
	}
	index := slices.IndexFunc(vm.PCIDevices, func(d pci.Decl) bool { return d.Name == deviceName })
	if index < 0 {
		return fmt.Errorf("no PCI device %q for VM %q", deviceName, vmName)
	}
	vm.PCIDevices = slices.Delete(vm.PCIDevices, index, index+1)
	vm.UpdatedAt = time.Now().UTC()
	return m.store.SaveVM(vm)
}

// ObservePCI reports whether each of the VM's PCI assignments can be used.
func (m *Manager) ObservePCI(vm *state.VMRecord) []PCIStatus {
	if len(vm.PCIDevices) == 0 {
		return nil
	}
	running := m.IsRunning(vm)
	statuses := make([]PCIStatus, len(vm.PCIDevices))
	for i, decl := range vm.PCIDevices {
		status := PCIStatus{Name: decl.Name, Selector: decl.Selector().String(), State: PCIReady}
		if running {
			status.State = PCIActive
		} else if err := pci.InspectDecl(decl).Err; err != nil {
			status.State = PCIUnavailable
			status.Error = err.Error()
		}
		statuses[i] = status
	}
	return statuses
}

func (m *Manager) preparePCIDevices(vm *state.VMRecord) error {
	if len(vm.PCIDevices) == 0 {
		return nil
	}
	if err := pciSupported(vm); err != nil {
		return err
	}
	for _, decl := range vm.PCIDevices {
		inspection := pci.InspectDecl(decl)
		if inspection.Err == nil {
			inspection.Err = pci.CheckGroups(inspection.Groups)
		}
		if inspection.Err != nil {
			return fmt.Errorf("PCI device %q: %w", decl.Name, inspection.Err)
		}
	}
	return CheckMemlock(vm.Resources.MemoryMiB)
}

// CheckMemlock reports whether this process's soft RLIMIT_MEMLOCK, which QEMU
// inherits, lets VFIO lock the memory of a guest with memoryMiB of RAM.
func CheckMemlock(memoryMiB int) error {
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_MEMLOCK, &limit); err != nil {
		return fmt.Errorf("read the memlock limit: %w", err)
	}
	return memlockError(limit.Cur, memoryMiB)
}

func memlockError(limit uint64, memoryMiB int) error {
	required := uint64(memoryMiB)<<20 + memlockMargin
	if limit == unix.RLIM_INFINITY || limit >= required {
		return nil
	}
	return fmt.Errorf("the memlock limit is %d MiB, but PCI passthrough for %d MiB of guest memory needs at least %d MiB; raise the soft and hard memlock limits (ulimit -l) for the Voom user", limit>>20, memoryMiB, required>>20)
}

func pciSupported(vm *state.VMRecord) error {
	if runtime.GOOS != "linux" || vm.Driver != "qemu" || vm.Arch != "x86_64-linux" {
		return fmt.Errorf("PCI passthrough is available only for x86_64 QEMU VMs on Linux; VM %q uses %s %s on %s", vm.Name, vm.Arch, vm.Driver, runtime.GOOS)
	}
	return nil
}
