package vm

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/state"
)

// AddForward declares a host-to-guest port forward on the VM and installs it
// in gvproxy if the VM is running.
func (m *Manager) AddForward(ctx context.Context, name string, guestPort, hostPort int, bind string, auto bool, protocol string) (forward.Decl, error) {
	protocol, err := forward.NormalizeProtocol(protocol)
	if err != nil {
		return forward.Decl{}, err
	}
	bind, err = forward.NormalizeBind(bind)
	if err != nil {
		return forward.Decl{}, err
	}
	if _, err := state.ParsePort(strconv.Itoa(guestPort)); err != nil {
		return forward.Decl{}, err
	}
	if hostPort != 0 {
		if _, err := state.ParsePort(strconv.Itoa(hostPort)); err != nil {
			return forward.Decl{}, err
		}
	}
	vm, unlock, err := m.store.LockVMRecord(ctx, name)
	if err != nil {
		return forward.Decl{}, err
	}
	defer unlock()
	var f forward.Decl
	err = m.store.WithGlobalVM(name, vm.ID, func() error {
		if auto {
			hostPort, err = m.allocateAutoPort(protocol, bind)
			if err != nil {
				return err
			}
		} else if hostPort == 0 {
			hostPort = guestPort
		}
		reserved, err := m.store.PortReserved(protocol, bind, hostPort)
		if err != nil {
			return err
		}
		if reserved || portBusy(protocol, bind, hostPort) {
			return fmt.Errorf("%s host port %s is unavailable", protocol, forward.Address(bind, hostPort))
		}
		f = forward.Decl{Protocol: protocol, GuestPort: guestPort, HostPort: hostPort, Bind: bind}
		vm.Network.Forwards = append(vm.Network.Forwards, f)
		vm.UpdatedAt = time.Now().UTC()
		if err := m.store.SaveVM(vm); err != nil {
			vm.Network.Forwards = vm.Network.Forwards[:len(vm.Network.Forwards)-1]
			return err
		}
		return nil
	})
	if err != nil {
		return forward.Decl{}, err
	}
	if m.IsRunning(vm) {
		rt := m.store.Runtime(vm)
		if err := gvproxy.Expose(rt.NetworkSock(), protocol, forward.Address(bind, hostPort), forward.Address(m.GuestTargetIP(vm), guestPort)); err != nil {
			vm.Network.Forwards = vm.Network.Forwards[:len(vm.Network.Forwards)-1]
			vm.UpdatedAt = time.Now().UTC()
			if saveErr := m.store.WithGlobalVM(name, vm.ID, func() error { return m.store.SaveVM(vm) }); saveErr != nil {
				return forward.Decl{}, fmt.Errorf("could not install forward: %w; additionally failed to roll back VM state: %v", err, saveErr)
			}
			return forward.Decl{}, err
		}
	}
	return f, nil
}

// RemoveForward removes the declared forward identified by protocol, host port, and bind
// and unexposes it from gvproxy if the VM is running.
func (m *Manager) RemoveForward(ctx context.Context, name string, port int, bind, protocol string) error {
	protocol, err := forward.NormalizeProtocol(protocol)
	if err != nil {
		return err
	}
	bind, err = forward.NormalizeBind(bind)
	if err != nil {
		return err
	}
	vm, unlock, err := m.store.LockVMRecord(ctx, name)
	if err != nil {
		return err
	}
	defer unlock()
	next := make([]forward.Decl, 0, len(vm.Network.Forwards))
	found := false
	for _, f := range vm.Network.Forwards {
		if f.Protocol == protocol && f.HostPort == port && f.Bind == bind {
			found = true
			continue
		}
		next = append(next, f)
	}
	if !found {
		return fmt.Errorf("no %s forward %s for VM %q", protocol, forward.Address(bind, port), name)
	}
	if m.IsRunning(vm) {
		if err := gvproxy.Unexpose(m.store.Runtime(vm).NetworkSock(), protocol, forward.Address(bind, port)); err != nil {
			return fmt.Errorf("could not remove %s forward: %w", protocol, err)
		}
	}
	previous := vm.Network.Forwards
	vm.Network.Forwards = next
	vm.UpdatedAt = time.Now().UTC()
	if err := m.store.WithGlobalVM(name, vm.ID, func() error { return m.store.SaveVM(vm) }); err != nil {
		if m.IsRunning(vm) {
			for _, f := range previous {
				if f.Protocol == protocol && f.Bind == bind && f.HostPort == port {
					err = errors.Join(err, gvproxy.Expose(m.store.Runtime(vm).NetworkSock(), protocol, forward.Address(bind, port), forward.Address(m.GuestTargetIP(vm), f.GuestPort)))
				}
			}
		}
		return err
	}
	return nil
}

// ForwardRows returns display rows for SSH, manual, and auto forwards across
// the given VM names, or across all VMs when args is empty.
func (m *Manager) ForwardRows(ctx context.Context, args []string) ([]ForwardRow, error) {
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
		autoRows, err := m.ReadRuntimeAutoForwards(vm)
		if err != nil {
			return nil, err
		}
		var installed map[gvproxy.Forward]struct{}
		var snapshotErr error
		if running {
			installed, snapshotErr = gvproxy.Forwards(ctx, m.store.Runtime(vm).NetworkSock())
		}
		observation := forwardObservation{running: running, installed: installed, err: snapshotErr}
		declarations := append([]forward.Decl{{Protocol: "tcp", Bind: vm.Network.SSHBind, HostPort: vm.Network.SSHPort, GuestPort: 22}}, vm.Network.Forwards...)
		for i, f := range declarations {
			kind := "manual"
			if i == 0 {
				kind = "ssh"
			}
			status, reason := observation.status(f, guestTargetIP)
			rows = append(rows, ForwardRow{VM: vm.Name, Kind: kind, Bind: f.Bind, HostPort: f.HostPort, GuestPort: f.GuestPort, GuestTargetIP: guestTargetIP, Protocol: f.Protocol, Status: status, Installed: status == "running", Reason: reason})
		}
		for _, f := range autoRows {
			status, reason := observation.autoStatus(f)
			rows = append(rows, ForwardRow{VM: vm.Name, Kind: "auto", Bind: f.Bind, HostPort: f.HostPort, GuestPort: f.GuestPort, GuestTargetIP: f.GuestTargetIP, Protocol: f.Protocol, Status: status, Installed: status == "active", Offset: f.Offset, Reason: reason})
		}
	}
	return rows, nil
}

// GuestTargetIP returns the IP address gvproxy uses to reach the guest for
// this VM's driver.
func (m *Manager) GuestTargetIP(vm *state.VMRecord) string {
	return state.DefaultGuestTargetIP(vm.Driver)
}
