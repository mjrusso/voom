package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/state"
)

func (m *Manager) forwardMutationPending(vm *state.VMRecord) bool {
	_, err := os.Stat(m.store.Runtime(vm).ForwardMutation())
	return !errors.Is(err, os.ErrNotExist)
}

// The caller must hold the VM lock.
func (m *Manager) recoverPendingForwardMutation(vm *state.VMRecord) error {
	_, err := m.recoverLiveForwardMutation(vm, m.pendingForwardMutationError(vm))
	return err
}

type forwardAction uint8

const (
	forwardExpose forwardAction = iota
	forwardUnexpose
)

type forwardRejectionPolicy uint8

const (
	forwardRejectBatch forwardRejectionPolicy = iota
	forwardSkipRejected
)

var errForwardRecoveryRequired = errors.New("forward mutation requires confirmed gvproxy termination")

func (m *Manager) pendingForwardMutationError(vm *state.VMRecord) error {
	if m.forwardMutationPending(vm) {
		return errForwardRecoveryRequired
	}
	return nil
}

type forwardOperation struct {
	action        forwardAction
	endpoint      forward.Decl
	guestTargetIP string
}

type forwardOutcome struct {
	operation forwardOperation
	err       error
}

var errForwardPortReserved = errors.New("host port is reserved by SSH or another forward")

type forwardMutation struct {
	rejection  forwardRejectionPolicy
	operations []forwardOperation
	commit     func([]forwardOutcome) error
}

// The caller must hold the VM lock. Only confirmed backend termination allows
// live callers to roll back declarations after a mutation requires recovery.
func (m *Manager) recoverLiveForwardMutation(vm *state.VMRecord, err error) (bool, error) {
	if !errors.Is(err, errForwardRecoveryRequired) {
		return true, err
	}
	stop, recoveryErr := m.recoverForwardMutation(vm, err)
	return stop.gatewayTerminationConfirmed, recoveryErr
}

func (m *Manager) executeForwardMutation(vm *state.VMRecord, mutation forwardMutation) error {
	if len(mutation.operations) == 0 {
		if mutation.commit != nil {
			return mutation.commit(nil)
		}
		return nil
	}
	outcomes, err := m.reserveForwardMutation(vm, mutation)
	if err != nil {
		return err
	}
	applied := false
	attempted := false
	for i := range outcomes {
		outcome := &outcomes[i]
		if outcome.err != nil {
			continue
		}
		attempted = true
		operation := outcome.operation
		local := forward.Address(operation.endpoint.Bind, operation.endpoint.HostPort)
		switch operation.action {
		case forwardExpose:
			remote := forward.Address(operation.guestTargetIP, operation.endpoint.GuestPort)
			err = gvproxy.Expose(m.store.Runtime(vm).NetworkSock(), operation.endpoint.Protocol, local, remote)
		case forwardUnexpose:
			err = gvproxy.Unexpose(m.store.Runtime(vm).NetworkSock(), operation.endpoint.Protocol, local)
		}
		if err == nil {
			applied = true
			continue
		}
		if errors.Is(err, gvproxy.ErrForwardOutcomeUnknown) || mutation.rejection == forwardRejectBatch {
			err = fmt.Errorf("%s %s forward %s: %w", operation.action, operation.endpoint.Protocol, local, err)
			break
		}
		outcome.err = err
		err = nil
	}
	if err == nil && mutation.commit != nil {
		err = mutation.commit(outcomes)
	}
	if !attempted {
		return err
	}
	if err != nil && (applied || errors.Is(err, gvproxy.ErrForwardOutcomeUnknown)) {
		err = errors.Join(errForwardRecoveryRequired, err)
	}
	if !errors.Is(err, errForwardRecoveryRequired) {
		cleanupErr := os.Remove(m.store.Runtime(vm).ForwardMutation())
		if cleanupErr == nil {
			return err
		}
		err = errors.Join(errForwardRecoveryRequired, err, cleanupErr)
	}
	return err
}

func (action forwardAction) String() string {
	if action == forwardUnexpose {
		return "unexpose"
	}
	return "expose"
}

// Journal publication and reservation validation share the global lock so a
// competing allocator cannot claim an endpoint between the two.
func (m *Manager) reserveForwardMutation(vm *state.VMRecord, mutation forwardMutation) ([]forwardOutcome, error) {
	outcomes := make([]forwardOutcome, 0, len(mutation.operations))
	err := m.store.WithGlobalVM(vm.Name, vm.ID, func() error {
		if err := m.pendingForwardMutationError(vm); err != nil {
			return err
		}
		stored, err := m.store.LoadVMByID(vm.ID)
		if err != nil {
			return err
		}
		owned, err := m.store.VMForwardReservations(stored, true)
		if err != nil {
			return err
		}
		claimed := make(map[forward.Decl]struct{}, len(owned))
		for _, endpoint := range owned {
			claimed[endpoint] = struct{}{}
		}
		reserved, err := m.store.PortReservations("")
		if err != nil {
			return err
		}
		var journal []forward.Decl
		for _, operation := range mutation.operations {
			if operation.action != forwardExpose && operation.action != forwardUnexpose {
				return errors.New("unsupported forward mutation action")
			}
			outcome := forwardOutcome{operation: operation}
			endpoint := operation.endpoint
			if _, exists := claimed[endpoint]; !exists {
				if operation.action == forwardUnexpose {
					return fmt.Errorf("forward endpoint %s/%s is not reserved by VM %q", endpoint.Protocol, forward.Address(endpoint.Bind, endpoint.HostPort), vm.Name)
				}
				if mutation.commit == nil {
					return errors.New("proposed forward requires a persistence commit")
				}
				if !reserved.Reserve(endpoint.Protocol, endpoint.Bind, endpoint.HostPort) {
					if mutation.rejection == forwardRejectBatch {
						return errForwardPortReserved
					}
					outcome.err = errForwardPortReserved
				}
			}
			if outcome.err == nil {
				journal = append(journal, endpoint)
			}
			outcomes = append(outcomes, outcome)
		}
		if len(journal) == 0 {
			return nil
		}
		return state.WriteJSONAtomic(m.store.Runtime(vm).ForwardMutation(), journal)
	})
	return outcomes, err
}

// The caller must hold the VM lock. A delayed expose can finish after unexpose,
// so only confirmed backend termination permits releasing its reservation.
func (m *Manager) recoverForwardMutation(vm *state.VMRecord, cause error) (runtimeStopResult, error) {
	confirmed, err := m.stopGVProxy(vm)
	if !confirmed {
		return runtimeStopResult{}, fmt.Errorf("forward reservation retained because gvproxy termination is unconfirmed: %w", errors.Join(cause, err))
	}
	recovery, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stop, stopErr := m.stopLocked(recovery, vm)
	if stopErr != nil {
		return stop, fmt.Errorf("gvproxy stopped during forward mutation recovery; VM cleanup failed: %w", errors.Join(cause, stopErr))
	}
	return stop, fmt.Errorf("VM stopped during forward mutation recovery: %w", cause)
}
