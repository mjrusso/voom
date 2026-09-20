package vm

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/mjrusso/voom/internal/driver/qemu"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
	"github.com/mjrusso/voom/internal/usb"
)

// USBHostState describes the configured route on the host.
type USBHostState string

// Host route states returned in USBHostStatus.
const (
	USBHostDisconnected USBHostState = "disconnected"
	USBHostConnected    USBHostState = "connected"
	USBHostInaccessible USBHostState = "inaccessible"
	USBHostUnknown      USBHostState = "unknown"
)

// USBRuntimeState describes the assignment in QEMU.
type USBRuntimeState string

// QEMU states returned in USBRuntimeStatus.
const (
	USBRuntimeStopped USBRuntimeState = "stopped"
	USBRuntimeMissing USBRuntimeState = "missing"
	USBRuntimeActive  USBRuntimeState = "active"
	USBRuntimeUnknown USBRuntimeState = "unknown"
)

// USBHostStatus contains the host route state and any inspection error.
type USBHostStatus struct {
	State USBHostState `json:"state"`
	Error string       `json:"error,omitempty"`
}

// USBRuntimeStatus contains the QEMU state and any monitor error.
type USBRuntimeStatus struct {
	State USBRuntimeState `json:"state"`
	Error string          `json:"error,omitempty"`
}

// USBStatus reports host and QEMU state for a configured USB assignment.
type USBStatus struct {
	Name     string           `json:"name"`
	Location string           `json:"location"`
	Host     USBHostStatus    `json:"host"`
	Runtime  USBRuntimeStatus `json:"runtime"`
}

// AddUSBDevice assigns a stable Linux USB topology route to a QEMU VM.
func (m *Manager) AddUSBDevice(ctx context.Context, vmName, deviceName, location string) (usb.Decl, error) {
	decl, err := usb.NewDecl(deviceName, location)
	if err != nil {
		return usb.Decl{}, err
	}
	vm, unlock, err := m.store.LockVMRecord(ctx, vmName)
	if err != nil {
		return usb.Decl{}, err
	}
	defer unlock()
	if runtime.GOOS != "linux" || vm.Driver != "qemu" {
		return usb.Decl{}, usbUnsupportedError(vm)
	}
	devices := append(append([]usb.Decl(nil), vm.USBDevices...), decl)
	if err := usb.ValidateAssignments(devices); err != nil {
		return usb.Decl{}, err
	}
	inventory, err := m.scanUSB()
	if err != nil {
		return usb.Decl{}, err
	}
	binding, err := inventory.Resolve(decl)
	if err != nil {
		return usb.Decl{}, err
	}
	if err := m.applyUSBTransition(ctx, vm, usbTransition{kind: usbAttach, device: decl, binding: binding, devices: devices}); err != nil {
		return usb.Decl{}, err
	}
	return decl, nil
}

// RemoveUSBDevice removes a USB assignment from a VM.
func (m *Manager) RemoveUSBDevice(ctx context.Context, vmName, deviceName string) error {
	vm, unlock, err := m.store.LockVMRecord(ctx, vmName)
	if err != nil {
		return err
	}
	defer unlock()
	index := -1
	for i, device := range vm.USBDevices {
		if device.Name == deviceName {
			index = i
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("no USB device %q for VM %q", deviceName, vmName)
	}
	running := m.IsRunning(vm)
	if running && (runtime.GOOS != "linux" || vm.Driver != "qemu") {
		return usbUnsupportedError(vm)
	}
	removed := vm.USBDevices[index]
	devices := append(append([]usb.Decl(nil), vm.USBDevices[:index]...), vm.USBDevices[index+1:]...)
	return m.applyUSBTransition(ctx, vm, usbTransition{kind: usbDetach, device: removed, devices: devices})
}

// ObserveUSB returns current status for the VM's configured USB assignments.
func (m *Manager) ObserveUSB(ctx context.Context, vm *state.VMRecord) []USBStatus {
	if len(vm.USBDevices) == 0 {
		return nil
	}
	statuses := make([]USBStatus, len(vm.USBDevices))
	inventory, inventoryErr := m.scanUSB()
	running := m.IsRunning(vm)
	active := map[string]bool{}
	var runtimeErr error
	if running {
		if runtime.GOOS != "linux" || vm.Driver != "qemu" {
			runtimeErr = usbUnsupportedError(vm)
		} else {
			active, runtimeErr = qemu.ActiveUSBDevices(ctx, m.store.Runtime(vm).QEMUMonitor(), vm.USBDevices)
		}
	}
	for i, decl := range vm.USBDevices {
		status := USBStatus{
			Name:     decl.Name,
			Location: decl.Location(),
			Host:     USBHostStatus{State: USBHostDisconnected},
			Runtime:  USBRuntimeStatus{State: USBRuntimeStopped},
		}
		var device usb.Device
		var connected bool
		inspectErr := inventoryErr
		if inspectErr == nil {
			device, connected, inspectErr = inventory.Inspect(decl)
		}
		if inspectErr != nil {
			status.Host.State = USBHostUnknown
			status.Host.Error = inspectErr.Error()
		} else if connected {
			status.Host.State = USBHostConnected
			if !device.Accessible {
				status.Host.State = USBHostInaccessible
				status.Host.Error = device.AccessError
			}
		}
		if running {
			status.Runtime.State = USBRuntimeMissing
			if runtimeErr != nil {
				status.Runtime.State = USBRuntimeUnknown
				status.Runtime.Error = runtimeErr.Error()
			} else if active[decl.Name] {
				status.Runtime.State = USBRuntimeActive
			}
		}
		statuses[i] = status
	}
	return statuses
}

func (m *Manager) prepareUSBDevices(vm *state.VMRecord) ([]usb.Binding, error) {
	if len(vm.USBDevices) == 0 {
		return nil, nil
	}
	if runtime.GOOS != "linux" || vm.Driver != "qemu" {
		return nil, usbUnsupportedError(vm)
	}
	inventory, err := m.scanUSB()
	if err != nil {
		return nil, err
	}
	bindings := make([]usb.Binding, 0, len(vm.USBDevices))
	for _, device := range vm.USBDevices {
		binding, err := inventory.Resolve(device)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, nil
}

func (m *Manager) withUSBStartReservation(ctx context.Context, vm *state.VMRecord, start func() error) error {
	if len(vm.USBDevices) == 0 {
		return start()
	}
	unlock, err := m.lockUSBRoutes(ctx, vm.USBDevices)
	if err != nil {
		return err
	}
	defer unlock()
	if err := m.store.WithGlobalVM(vm.Name, vm.ID, func() error {
		if err := m.checkUSBConflicts(vm, vm.USBDevices); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	return start()
}

func (m *Manager) checkUSBConflicts(vm *state.VMRecord, requested []usb.Decl) error {
	vms, err := m.store.ListVMs()
	if err != nil {
		return err
	}
	for _, other := range vms {
		if other.ID == vm.ID || !m.usbRuntimeMayBeActive(other) {
			continue
		}
		for _, candidate := range requested {
			for _, attached := range other.USBDevices {
				if candidate.Route == attached.Route {
					return fmt.Errorf("USB location %s is reserved by VM %q with active or unconfirmed runtime", candidate.Location(), other.Name)
				}
			}
		}
	}
	return nil
}

func (m *Manager) usbRuntimeMayBeActive(vm *state.VMRecord) bool {
	rt := m.store.Runtime(vm)
	if process.HasRecord(rt.VMProcessRecord()) {
		return true
	}
	for _, artifact := range rt.DriverArtifacts(vm.Driver) {
		if socketExists(artifact) {
			return true
		}
	}
	return false
}

type usbChangeKind uint8

const (
	usbAttach usbChangeKind = iota
	usbDetach
)

type usbTransition struct {
	kind    usbChangeKind
	device  usb.Decl
	binding usb.Binding
	devices []usb.Decl
}

func (m *Manager) applyUSBTransition(ctx context.Context, vm *state.VMRecord, transition usbTransition) error {
	candidate := *vm
	candidate.USBDevices = transition.devices
	candidate.UpdatedAt = time.Now().UTC()
	unlock, err := m.lockUSBRoutes(ctx, []usb.Decl{transition.device})
	if err != nil {
		return err
	}
	defer unlock()
	running := m.IsRunning(vm)
	if !running {
		if m.usbRuntimeMayBeActive(vm) {
			return fmt.Errorf("cannot change USB assignments for VM %q while its runtime state is unconfirmed", vm.Name)
		}
		return m.saveUSBRecord(vm, candidate)
	}
	previous := *vm
	if transition.kind == usbAttach {
		if err := m.store.WithGlobalVM(vm.Name, vm.ID, func() error {
			if err := m.checkUSBConflicts(vm, []usb.Decl{transition.device}); err != nil {
				return err
			}
			return m.writeUSBRecord(vm, candidate)
		}); err != nil {
			return err
		}
	}

	socket := m.store.Runtime(vm).QEMUMonitor()
	var change qemu.USBChangeResult
	switch transition.kind {
	case usbAttach:
		change = qemu.AttachUSB(ctx, socket, transition.binding)
	case usbDetach:
		change = qemu.DetachUSB(ctx, socket, transition.device)
	default:
		return errors.New("invalid USB transition")
	}

	switch change.Outcome {
	case qemu.USBChangeApplied:
		if transition.kind == usbDetach {
			if err := m.saveUSBRecord(vm, candidate); err != nil {
				return fmt.Errorf("QEMU detached USB device %q from VM %q, but Voom could not save the removal. Retry the removal: %w", transition.device.Name, vm.Name, err)
			}
		}
		return nil
	case qemu.USBChangeNotApplied:
		if transition.kind == usbAttach {
			return m.restoreUSBAttachState(vm, previous, transition.binding, change.Err)
		}
		return change.Err
	case qemu.USBChangeUnknown:
		if transition.kind == usbAttach {
			recovery, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			rollback := qemu.DetachUSB(recovery, socket, transition.device)
			if rollback.Outcome != qemu.USBChangeApplied {
				return errors.Join(change.Err, fmt.Errorf("USB configuration remains updated because runtime rollback could not be confirmed: %w", rollback.Err))
			}
			return m.restoreUSBAttachState(vm, previous, transition.binding, change.Err)
		}
		return errors.Join(change.Err, fmt.Errorf("USB removal completion could not be confirmed; assignment %q remains reserved for VM %q; retry the removal", transition.device.Name, vm.Name))
	default:
		return errors.New("QEMU returned an invalid USB change outcome")
	}
}

func (m *Manager) saveUSBRecord(vm *state.VMRecord, candidate state.VMRecord) error {
	return m.store.WithGlobalVM(vm.Name, vm.ID, func() error {
		return m.writeUSBRecord(vm, candidate)
	})
}

func (m *Manager) writeUSBRecord(vm *state.VMRecord, candidate state.VMRecord) error {
	if err := m.store.SaveVM(&candidate); err != nil {
		return err
	}
	*vm = candidate
	return nil
}

func (m *Manager) restoreUSBAttachState(vm *state.VMRecord, previous state.VMRecord, binding usb.Binding, cause error) error {
	if rollbackErr := m.saveUSBRecord(vm, previous); rollbackErr != nil {
		recovery, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		restore := qemu.AttachUSB(recovery, m.store.Runtime(vm).QEMUMonitor(), binding)
		return errors.Join(cause, fmt.Errorf("restore VM state after USB runtime change failed: %w", rollbackErr), restore.Err)
	}
	return cause
}

func (m *Manager) lockUSBRoutes(ctx context.Context, devices []usb.Decl) (func(), error) {
	resources := make([]string, 0, len(devices))
	for _, device := range devices {
		resources = append(resources, "usb:"+device.Route.Location())
	}
	return m.store.LockResources(ctx, resources)
}

func usbUnsupportedError(vm *state.VMRecord) error {
	return fmt.Errorf("USB passthrough is available only for QEMU on Linux; VM %q uses %s on %s", vm.Name, vm.Driver, runtime.GOOS)
}
