package vm

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

func (m *Manager) applyForwardDeclarations(ctx context.Context, vm *state.VMRecord, proposal forward.MutationJournal) (forward.MutationJournal, error) {
	journal := proposal
	journal.Requests = slices.Clone(proposal.Requests)
	if proposal.AutoForwards != nil {
		rows := slices.Clone(*proposal.AutoForwards)
		journal.AutoForwards = &rows
	}
	if len(journal.Requests) > 0 {
		ids, err := gvproxy.AllocateForwardIDs(ctx, m.store.Runtime(vm).NetworkSock(), len(journal.Requests))
		if err != nil {
			return journal, err
		}
		for i := range journal.Requests {
			journal.Requests[i].ID, journal.Requests[i].Result = ids[i], forward.UnsubmittedMutation()
		}
	}
	// Reservation checks and publication must share the global lock.
	err := m.store.WithGlobalVMContext(ctx, vm.Name, vm.ID, func() error {
		readReservations := m.store.PortReservations
		if slices.ContainsFunc(journal.Requests, forward.MutationRequest.Exposes) {
			read, err := m.store.CheckPortReservations()
			if err != nil {
				return err
			}
			readReservations = read
		}
		owned, err := m.ownedForwardEndpoints(vm)
		if err != nil {
			return err
		}
		switch {
		case proposal.Manual != nil:
			intent, err := prepareManualDeclaration(*proposal.Manual, owned, readReservations)
			if err != nil {
				return err
			}
			journal.Manual, journal.Requests[0].Endpoint = &intent, intent.Endpoint
		case journal.AutoForwards != nil:
			if err := prepareAutomaticDeclarations(&journal, owned, readReservations); err != nil {
				return err
			}
		default:
			for _, request := range journal.Requests {
				if !slices.Contains(owned, request.Endpoint) {
					return fmt.Errorf("forward endpoint %s/%s is not reserved by VM %q", request.Endpoint.Protocol, forward.Address(request.Endpoint.Bind, request.Endpoint.HostPort), vm.Name)
				}
			}
		}
		if len(journal.Requests) == 0 {
			return nil
		}
		journal.Version = 1
		journal.Process, err = process.RecordFingerprint(m.store.Runtime(vm).GVProxyProcessRecord())
		if err != nil {
			return err
		}
		if err := state.WriteJSONAtomic(m.store.Runtime(vm).ForwardMutation(), journal); err != nil {
			return err
		}
		if err := m.saveManualDeclaration(vm, journal.Manual); err != nil {
			return errors.Join(errForwardRecoveryRequired, err)
		}
		return nil
	})
	if err == nil {
		_, err = m.processForwardJournal(ctx, vm, &journal)
	}
	return journal, err
}

// The caller holds the VM and global locks.
func (m *Manager) ownedForwardEndpoints(vm *state.VMRecord) ([]forward.Decl, error) {
	if err := m.pendingForwardMutationError(vm); err != nil {
		return nil, err
	}
	stored, err := m.store.LoadVMByID(vm.ID)
	if err != nil {
		return nil, err
	}
	return m.store.VMForwardReservations(stored, true)
}

func (m *Manager) editStoppedForward(ctx context.Context, vm *state.VMRecord, intent forward.ManualMutation) (forward.Decl, error) {
	err := m.store.WithGlobalVMContext(ctx, vm.Name, vm.ID, func() error {
		owned, err := m.ownedForwardEndpoints(vm)
		if err != nil {
			return err
		}
		intent, err = prepareManualDeclaration(intent, owned, m.store.PortReservations)
		if err != nil {
			return err
		}
		return m.saveManualDeclaration(vm, &intent)
	})
	return intent.Endpoint, err
}

func prepareManualDeclaration(intent forward.ManualMutation, owned []forward.Decl, readReservations func(string) (state.PortReservations, error)) (forward.ManualMutation, error) {
	endpoint := intent.Endpoint
	if intent.Kind == forward.ManualUnexpose {
		if !slices.Contains(owned, endpoint) {
			return intent, errors.New("forward endpoint is not owned by this VM")
		}
		return intent, nil
	}
	reserved, err := readReservations("")
	if err != nil {
		return intent, err
	}
	if endpoint.HostPort == 0 {
		endpoint.HostPort, err = allocateAutoPort(reserved, endpoint.Protocol, endpoint.Bind)
		if err != nil {
			return intent, err
		}
	}
	if portBusy(endpoint.Protocol, endpoint.Bind, endpoint.HostPort) || !reserved.Reserve(endpoint.Protocol, endpoint.Bind, endpoint.HostPort) {
		return intent, fmt.Errorf("%s host port %s is unavailable", endpoint.Protocol, forward.Address(endpoint.Bind, endpoint.HostPort))
	}
	intent.Endpoint = endpoint
	return intent, nil
}

func prepareAutomaticDeclarations(journal *forward.MutationJournal, owned []forward.Decl, readReservations func(string) (state.PortReservations, error)) error {
	var reserved *state.PortReservations
	accepted := make([]forward.MutationRequest, 0, len(journal.Requests))
	for _, request := range journal.Requests {
		if slices.Contains(owned, request.Endpoint) {
			accepted = append(accepted, request)
			continue
		}
		if !request.Exposes() {
			return errors.New("automatic removal is not owned by this VM")
		}
		if reserved == nil {
			snapshot, err := readReservations("")
			if err != nil {
				return err
			}
			reserved = &snapshot
		}
		endpoint := request.Endpoint
		if reserved.Reserve(endpoint.Protocol, endpoint.Bind, endpoint.HostPort) {
			accepted = append(accepted, request)
			continue
		}
		for i := range *journal.AutoForwards {
			row := &(*journal.AutoForwards)[i]
			if row.Endpoint() == endpoint {
				row.Status, row.Reason, row.GVProxyPID = forward.ReservationSkipped, errForwardPortReserved.Error(), 0
			}
		}
	}
	journal.Requests = accepted
	return nil
}
