package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

func (m *Manager) forwardMutationPending(vm *state.VMRecord) bool {
	_, err := os.Stat(m.store.Runtime(vm).ForwardMutation())
	return !errors.Is(err, os.ErrNotExist)
}

// The caller must hold the VM lock.
func (m *Manager) recoverPendingForwardMutation(vm *state.VMRecord) error {
	if !m.forwardMutationPending(vm) {
		_, err := m.ObserveRuntime(vm)
		return err
	}
	_, err := m.recoverAndReportForwardMutation(vm, m.pendingForwardMutationError(vm))
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

var errForwardRecoveryRequired = errors.New("forward mutation requires recovery")

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

type forwardMutationFailure struct {
	operation forwardOperation
	err       error
}

func (e *forwardMutationFailure) Error() string {
	return fmt.Sprintf("%s %s forward %s: %v", e.operation.action, e.operation.endpoint.Protocol,
		forward.Address(e.operation.endpoint.Bind, e.operation.endpoint.HostPort), e.err)
}

func (e *forwardMutationFailure) Unwrap() error { return e.err }

var errForwardPortReserved = errors.New("host port is reserved by SSH or another forward")

type forwardMutation struct {
	rejection  forwardRejectionPolicy
	operations []forwardOperation
	commit     func([]forwardOutcome) error
}

type forwardRecoveryFailure struct {
	outcome string
	err     error
}

func (e *forwardRecoveryFailure) Error() string { return e.err.Error() }
func (e *forwardRecoveryFailure) Unwrap() error { return e.err }

func (m *Manager) recoverAndReportForwardMutation(vm *state.VMRecord, err error) (bool, error) {
	releaseSafe, err := m.recoverForwardMutation(vm, err)
	var failure *forwardRecoveryFailure
	if errors.As(err, &failure) {
		m.recordForwardRecovery(vm, failure.outcome, err)
	}
	return releaseSafe, err
}

// The caller must hold the VM lock. Only confirmed driver exit permits helper
// teardown; failed identity inspection does not prove exit. A snapshot cannot
// fence a delayed expose, so unresolved mutations must retain reservations.
func (m *Manager) recoverForwardMutation(vm *state.VMRecord, err error) (bool, error) {
	if !errors.Is(err, errForwardRecoveryRequired) {
		return true, err
	}
	observed, inspectionErr := process.ObserveRecord(m.store.Runtime(vm).VMProcessRecord(), state.VMProcessKind(vm.Driver), "")
	if inspectionErr != nil || observed.State != process.RecordExited {
		return false, &forwardRecoveryFailure{outcome: "degraded", err: fmt.Errorf("forwarding change could not be confirmed; affected ports reserved: %w", errors.Join(err, inspectionErr))}
	}
	recovery, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stop, cleanupErr := m.cleanupConfirmedExitLocked(recovery, vm)
	return stop.gatewayTerminationConfirmed, &forwardRecoveryFailure{outcome: "cleanup-after-exit", err: errors.Join(err, cleanupErr)}
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
			err = &forwardMutationFailure{operation: operation, err: err}
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
