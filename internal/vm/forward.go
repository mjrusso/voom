package vm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/state"
)

func declaredForwards(vm *state.VMRecord) []forward.Decl {
	return append([]forward.Decl{{Protocol: "tcp", Bind: vm.Network.SSHBind, HostPort: vm.Network.SSHPort, GuestPort: 22}}, vm.Network.Forwards...)
}

// The caller must hold the VM lock. Failed startup owns runtime cleanup.
func (m *Manager) installDeclaredForwardsLocked(ctx context.Context, vm *state.VMRecord) error {
	var requests []forward.MutationRequest
	for _, endpoint := range declaredForwards(vm) {
		requests = append(requests, forward.MutationRequest{Kind: forward.ManualExpose, Endpoint: endpoint, GuestTargetIP: m.GuestTargetIP(vm)})
	}
	_, err := m.applyForwardDeclarations(ctx, vm, forward.MutationJournal{Requests: requests})
	return err
}

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
	if err := m.recoverPendingForwardMutation(ctx, vm); err != nil {
		return forward.Decl{}, err
	}
	observed, err := m.observeCurrentRuntime(ctx, vm)
	if err != nil {
		return forward.Decl{}, err
	}
	if auto {
		hostPort = 0
	} else if hostPort == 0 {
		hostPort = guestPort
	}
	intent := forward.ManualMutation{Kind: forward.ManualExpose, Endpoint: forward.Decl{Protocol: protocol, GuestPort: guestPort, HostPort: hostPort, Bind: bind}}
	if observed == RuntimeStopped {
		return m.editStoppedForward(ctx, vm, intent)
	}
	journal, err := m.applyForwardDeclarations(ctx, vm, forward.MutationJournal{
		Requests: []forward.MutationRequest{{Kind: intent.Kind, Endpoint: intent.Endpoint, GuestTargetIP: m.GuestTargetIP(vm)}},
		Manual:   &intent,
	})
	_, err = m.recoverAndReportForwardMutation(ctx, vm, err)
	if err != nil {
		return forward.Decl{}, err
	}
	return journal.Manual.Endpoint, nil
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
	var removed forward.Decl
	found := false
	for _, f := range vm.Network.Forwards {
		if f.Protocol == protocol && f.HostPort == port && f.Bind == bind {
			removed = f
			found = true
			continue
		}
	}
	if !found {
		return fmt.Errorf("no %s forward %s for VM %q", protocol, forward.Address(bind, port), name)
	}
	if err := m.recoverPendingForwardMutation(ctx, vm); err != nil {
		return err
	}
	if !slices.Contains(vm.Network.Forwards, removed) {
		return nil
	}
	intent := forward.ManualMutation{Kind: forward.ManualUnexpose, Endpoint: removed}
	if !m.gvproxyMayBeRunning(vm) {
		_, err := m.editStoppedForward(ctx, vm, intent)
		return err
	}
	_, mutationErr := m.applyForwardDeclarations(ctx, vm, forward.MutationJournal{
		Requests: []forward.MutationRequest{{Kind: intent.Kind, Endpoint: removed}},
		Manual:   &intent,
	})
	_, err = m.recoverAndReportForwardMutation(ctx, vm, mutationErr)
	if err != nil {
		return fmt.Errorf("could not remove %s forward: %w", protocol, err)
	}
	if slices.Contains(vm.Network.Forwards, removed) {
		return errors.Join(errors.New("forward removal was not saved"), mutationErr)
	}
	return nil
}

// ForwardRows reports configured forwards and unresolved reservations for the
// given VM names, or for all VMs when args is empty.
func (m *Manager) ForwardRows(ctx context.Context, args []string) ([]ForwardRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		runtimeState, inspectionErr := m.ObserveRuntimeContext(ctx, vm)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		running := runtimeState == RuntimeRunning
		guestTargetIP := state.DefaultGuestTargetIP(vm.Driver)
		if running {
			guestTargetIP = m.GuestTargetIP(vm)
		}
		autoRows, err := m.ReadRuntimeAutoForwards(vm)
		if err != nil {
			return nil, err
		}
		journal, err := forward.ReadMutationJournal(m.store.Runtime(vm).ForwardMutation())
		if err != nil {
			return nil, err
		}
		var installed map[gvproxy.Forward]struct{}
		snapshotErr := inspectionErr
		if running && journal == nil {
			installed, snapshotErr = gvproxy.Forwards(ctx, m.store.Runtime(vm).NetworkSock())
		}
		observation := forwardObservation{running: running, installed: installed, err: snapshotErr}
		if journal != nil {
			observation.recoveryReason = "confirmation pending; affected ports remain reserved. Run 'voom forward reconcile " + vm.Name + "' to retry"
			if journal.Version == 0 {
				observation.recoveryReason = "recovery details are unavailable; stop and start the VM to clear this state"
			}
			for _, request := range journal.Requests {
				if request.Error != "" {
					observation.recoveryReason += ": " + request.Error
					break
				}
			}
			if inspectionErr != nil {
				observation.recoveryReason += "; " + inspectionErr.Error()
			}
		}
		declarations := declaredForwards(vm)
		known := make(map[forward.Decl]struct{})
		for i, f := range declarations {
			known[f] = struct{}{}
			kind := "manual"
			if i == 0 {
				kind = "ssh"
			}
			status, reason := observation.status(f, guestTargetIP)
			rows = append(rows, ForwardRow{VM: vm.Name, Kind: kind, Bind: f.Bind, HostPort: f.HostPort, GuestPort: f.GuestPort, GuestTargetIP: guestTargetIP, Protocol: f.Protocol, Status: status, Installed: status == "running", Reason: reason})
		}
		for _, f := range autoRows {
			known[f.Endpoint()] = struct{}{}
			status, reason := observation.autoStatus(f)
			rows = append(rows, ForwardRow{VM: vm.Name, Kind: "auto", Bind: f.Bind, HostPort: f.HostPort, GuestPort: f.GuestPort, GuestTargetIP: f.GuestTargetIP, Protocol: f.Protocol, Status: status, Installed: status == "active", Offset: f.Offset, Reason: reason})
		}
		if journal != nil {
			for _, f := range journal.Reservations {
				if _, represented := known[f]; represented {
					continue
				}
				known[f] = struct{}{}
				status, reason := observation.status(f, guestTargetIP)
				rows = append(rows, ForwardRow{VM: vm.Name, Kind: "reservation", Bind: f.Bind, HostPort: f.HostPort, GuestPort: f.GuestPort, GuestTargetIP: guestTargetIP, Protocol: f.Protocol, Status: status, Reason: reason})
			}
		}
	}
	return rows, nil
}

// GuestTargetIP returns the IP address gvproxy uses to reach the guest for
// this VM's driver.
func (m *Manager) GuestTargetIP(vm *state.VMRecord) string {
	return state.DefaultGuestTargetIP(vm.Driver)
}
