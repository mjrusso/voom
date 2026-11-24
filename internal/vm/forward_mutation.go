package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

func (m *Manager) forwardMutationPending(vm *state.VMRecord) bool {
	_, err := os.Stat(m.store.Runtime(vm).ForwardMutation())
	return !errors.Is(err, os.ErrNotExist)
}

// The caller must hold the VM lock.
func (m *Manager) recoverPendingForwardMutation(ctx context.Context, vm *state.VMRecord) error {
	if err := state.CheckPreviousVMRuntime(m.previousRuntime, vm); err != nil {
		return err
	}
	if !m.forwardMutationPending(vm) {
		_, err := m.observeCurrentRuntime(ctx, vm)
		return err
	}
	releaseSafe, err := m.recoverAndReportForwardMutation(ctx, vm, m.pendingForwardMutationError(vm))
	// Settled rejections from earlier commands must not block this mutation.
	if releaseSafe && !errors.Is(err, errForwardRecoveryRequired) {
		return nil
	}
	return err
}

var errForwardRecoveryRequired = errors.New("forward mutation requires recovery")

func (m *Manager) pendingForwardMutationError(vm *state.VMRecord) error {
	if m.forwardMutationPending(vm) {
		return errForwardRecoveryRequired
	}
	return nil
}

type forwardMutationFailure struct {
	request forward.MutationRequest
	err     error
}

func (e *forwardMutationFailure) Error() string {
	return fmt.Sprintf("%s %s forward %s: %v", e.request.Action(), e.request.Endpoint.Protocol,
		forward.Address(e.request.Endpoint.Bind, e.request.Endpoint.HostPort), e.err)
}

func (e *forwardMutationFailure) Unwrap() error { return e.err }

var errForwardPortReserved = errors.New("host port is reserved by SSH or another forward")

type forwardRecoveryFailure struct {
	outcome string
	err     error
}

func (e *forwardRecoveryFailure) Error() string { return e.err.Error() }
func (e *forwardRecoveryFailure) Unwrap() error { return e.err }

func (m *Manager) recoverAndReportForwardMutation(ctx context.Context, vm *state.VMRecord, err error) (bool, error) {
	releaseSafe, err := m.recoverForwardMutation(ctx, vm, err)
	var failure *forwardRecoveryFailure
	if errors.As(err, &failure) {
		m.recordForwardRecovery(vm, failure.outcome, err)
	}
	return releaseSafe, err
}

// The caller must hold the VM lock. Only confirmed driver exit permits helper
// teardown; failed identity inspection does not prove exit. A snapshot cannot
// fence a delayed expose, so unresolved mutations must retain reservations.
func (m *Manager) recoverForwardMutation(ctx context.Context, vm *state.VMRecord, err error) (bool, error) {
	if !errors.Is(err, errForwardRecoveryRequired) {
		return true, err
	}
	observed, inspectionErr := process.ObserveRecord(m.store.Runtime(vm).VMProcessRecord(), state.VMProcessKind(vm.Driver), "")
	if inspectionErr == nil && observed.State == process.RecordRunning {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		complete, recoveryErr := m.resolvePendingForwardMutation(ctx, vm)
		if complete {
			return true, recoveryErr
		}
		inspectionErr = recoveryErr
	}
	if inspectionErr != nil || observed.State != process.RecordExited {
		return false, &forwardRecoveryFailure{outcome: "degraded", err: fmt.Errorf("forwarding change could not be confirmed; affected ports remain reserved; run 'voom forward reconcile %s': %w", vm.Name, errors.Join(err, inspectionErr))}
	}
	recovery, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	stop, cleanupErr := m.cleanupConfirmedExitLocked(recovery, vm)
	return stop.gatewayTerminationConfirmed, &forwardRecoveryFailure{outcome: "cleanup-after-exit", err: errors.Join(err, cleanupErr)}
}
