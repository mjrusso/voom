package vm

import (
	"fmt"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/state"
)

// AddForward declares a host-to-guest port forward on the VM and installs it
// in gvproxy if the VM is running.
func (m *Manager) AddForward(name string, guestPort, hostPort int, bind string, auto bool) (forward.Decl, error) {
	bind, err := forward.NormalizeBind(bind)
	if err != nil {
		return forward.Decl{}, err
	}
	vm, err := m.store.LoadVM(name)
	if err != nil {
		return forward.Decl{}, err
	}
	unlock, err := m.store.LockVM(vm.ID)
	if err != nil {
		return forward.Decl{}, err
	}
	defer unlock()
	if auto {
		hostPort, err = m.allocateAutoPort(bind)
		if err != nil {
			return forward.Decl{}, err
		}
	} else if hostPort == 0 {
		hostPort = guestPort
	}
	reserved, err := m.store.PortReserved(bind, hostPort)
	if err != nil {
		return forward.Decl{}, err
	}
	if reserved || portBusy(bind, hostPort) {
		return forward.Decl{}, fmt.Errorf("host port %s:%d is unavailable", bind, hostPort)
	}
	f := forward.Decl{Protocol: "tcp", GuestPort: guestPort, HostPort: hostPort, Bind: bind}
	vm.Network.Forwards = append(vm.Network.Forwards, f)
	vm.UpdatedAt = time.Now().UTC()
	if err := m.store.SaveVM(vm); err != nil {
		return forward.Decl{}, err
	}
	if m.IsRunning(vm) {
		rt := m.store.Runtime(vm)
		if err := gvproxyExpose(rt.NetworkSock(), fmt.Sprintf("%s:%d", bind, hostPort), fmt.Sprintf("%s:%d", m.GuestTargetIP(vm), guestPort)); err != nil {
			vm.Network.Forwards = vm.Network.Forwards[:len(vm.Network.Forwards)-1]
			vm.UpdatedAt = time.Now().UTC()
			if saveErr := m.store.SaveVM(vm); saveErr != nil {
				return forward.Decl{}, fmt.Errorf("could not install forward: %w; additionally failed to roll back VM state: %v", err, saveErr)
			}
			return forward.Decl{}, err
		}
	}
	return f, nil
}

// RemoveForward removes the declared forward identified by host port and bind
// and unexposes it from gvproxy if the VM is running.
func (m *Manager) RemoveForward(name string, port int, bind string) error {
	bind, err := forward.NormalizeBind(bind)
	if err != nil {
		return err
	}
	vm, err := m.store.LoadVM(name)
	if err != nil {
		return err
	}
	unlock, err := m.store.LockVM(vm.ID)
	if err != nil {
		return err
	}
	defer unlock()
	next := vm.Network.Forwards[:0]
	found := false
	for _, f := range vm.Network.Forwards {
		if f.HostPort == port && f.Bind == bind {
			found = true
			continue
		}
		next = append(next, f)
	}
	if !found {
		return fmt.Errorf("no forward %s:%d for VM %q", bind, port, name)
	}
	vm.Network.Forwards = next
	vm.UpdatedAt = time.Now().UTC()
	if err := m.store.SaveVM(vm); err != nil {
		return err
	}
	if m.IsRunning(vm) {
		_ = gvproxyUnexpose(m.store.Runtime(vm).NetworkSock(), fmt.Sprintf("%s:%d", bind, port))
	}
	return nil
}

// ForwardRows returns display rows for SSH, manual, and auto forwards across
// the given VM names, or across all VMs when args is empty.
func (m *Manager) ForwardRows(args []string) ([]ForwardRow, error) {
	var vms []*state.VMRecord
	if len(args) == 1 {
		vm, err := m.store.LoadVM(args[0])
		if err != nil {
			return nil, err
		}
		vms = []*state.VMRecord{vm}
	} else {
		var err error
		vms, err = m.store.ListVMs()
		if err != nil {
			return nil, err
		}
	}
	rows := []ForwardRow{}
	for _, vm := range vms {
		running := m.IsRunning(vm)
		guestTargetIP := state.DefaultGuestTargetIP(vm.Driver)
		if running {
			guestTargetIP = m.GuestTargetIP(vm)
		}
		rows = append(rows, ForwardRow{VM: vm.Name, Kind: "ssh", Bind: vm.Network.SSHBind, HostPort: vm.Network.SSHPort, GuestPort: 22, GuestTargetIP: guestTargetIP, Protocol: "tcp", Status: Status(running), Installed: running})
		for _, f := range vm.Network.Forwards {
			rows = append(rows, ForwardRow{VM: vm.Name, Kind: "manual", Bind: f.Bind, HostPort: f.HostPort, GuestPort: f.GuestPort, GuestTargetIP: guestTargetIP, Protocol: f.Protocol, Status: Status(running), Installed: running})
		}
		autoRows, err := m.ReadRuntimeAutoForwards(vm)
		if err != nil {
			return nil, err
		}
		for _, f := range autoRows {
			rows = append(rows, ForwardRow{VM: vm.Name, Kind: "auto", Bind: f.Bind, HostPort: f.HostPort, GuestPort: f.GuestPort, GuestTargetIP: f.GuestTargetIP, Protocol: f.Protocol, Status: f.Status, Installed: f.Installed, Offset: f.Offset, Reason: f.Reason})
		}
	}
	return rows, nil
}

// GuestTargetIP returns the IP address gvproxy uses to reach the guest for
// this VM's driver.
func (m *Manager) GuestTargetIP(vm *state.VMRecord) string {
	return state.DefaultGuestTargetIP(vm.Driver)
}
