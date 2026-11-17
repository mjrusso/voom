package vm

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

func (m *Manager) startRuntimeWatcher(vm *state.VMRecord) error {
	rt := m.store.Runtime(vm)
	if err := m.stopLegacyAutoForwardWatcher(vm); err != nil {
		return err
	}
	fingerprint, err := process.RecordFingerprint(rt.VMProcessRecord())
	if err != nil {
		return err
	}
	return m.startSelfRecorded([]string{"debug", "watch-vm", vm.ID, fingerprint}, m.store.LogPath(vm, "runtime-watch"), rt.RuntimeWatcherProcessRecord())
}

func (m *Manager) stopRuntimeWatcher(vm *state.VMRecord) error {
	path := m.store.Runtime(vm).RuntimeWatcherProcessRecord()
	if pid, ok := process.ValidRecord(path, "runtime-watch"); ok && pid == os.Getpid() {
		return nil
	}
	return process.StopRecorded(path, "runtime-watch", processStopTimeout)
}

func (m *Manager) stopLegacyAutoForwardWatcher(vm *state.VMRecord) error {
	return process.StopRecorded(m.store.Runtime(vm).LegacyAutoForwardProcessRecord(), "auto-forward", processStopTimeout)
}

type runtimeWatchOutcome uint8

const (
	runtimeWatchSkipped runtimeWatchOutcome = iota
	runtimeWatchContinue
	runtimeWatchDone
)

const (
	runtimeDiscoveryInterval = 2 * time.Second
	runtimeAttemptTimeout    = 2 * time.Second
)

type runtimeWatchSchedule struct {
	nextDiscovery time.Time
	nextRecovery  time.Time
	recoveryState process.RecordState
	retryDelay    time.Duration
}

func (s *runtimeWatchSchedule) recoveryAttempt(err error) {
	if err == nil {
		s.nextRecovery, s.retryDelay = time.Time{}, 0
		return
	}
	if s.retryDelay == 0 {
		s.retryDelay = 2 * time.Second
	}
	s.nextRecovery = time.Now().Add(s.retryDelay)
	s.retryDelay = min(2*s.retryDelay, 30*time.Second)
}

// WatchRuntime reconciles forwarding and cleans up after confirmed driver exit.
func (m *Manager) WatchRuntime(ctx context.Context, id, fingerprint string) error {
	vm, err := m.store.LoadVMByID(id)
	if err != nil {
		return err
	}
	rt := m.store.Runtime(vm)
	defer func() {
		// A newer start may have installed a replacement watcher at this path.
		if pid, ok := process.ValidRecord(rt.RuntimeWatcherProcessRecord(), "runtime-watch"); ok && pid == os.Getpid() {
			_ = process.RemoveRecord(rt.RuntimeWatcherProcessRecord())
		}
	}()
	schedule := runtimeWatchSchedule{nextDiscovery: time.Now()}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	lastError := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		outcome, err := m.watchRuntimeOnce(ctx, id, fingerprint, &schedule)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if outcome == runtimeWatchDone {
			return err
		}
		if outcome != runtimeWatchSkipped {
			lastError = m.logForwardWatchResult(vm, err, lastError)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *Manager) watchRuntimeOnce(ctx context.Context, id, fingerprint string, schedule *runtimeWatchSchedule) (runtimeWatchOutcome, error) {
	unlock, err := m.store.TryLockVM(id)
	if err != nil {
		return runtimeWatchContinue, err
	}
	if unlock == nil {
		return runtimeWatchSkipped, nil
	}
	defer unlock()
	vm, err := m.store.LoadVMByID(id)
	if errors.Is(err, os.ErrNotExist) {
		return runtimeWatchDone, nil
	}
	if err != nil {
		return runtimeWatchContinue, err
	}
	now := time.Now()
	if schedule.recoveryState == process.RecordUnknown && now.Before(schedule.nextRecovery) {
		return runtimeWatchSkipped, nil
	}
	observed, err := process.ObserveRecord(m.store.Runtime(vm).VMProcessRecord(), state.VMProcessKind(vm.Driver), fingerprint)
	// Exit cleanup must not inherit a forwarding retry's deadline.
	if observed.State != schedule.recoveryState {
		schedule.recoveryState = observed.State
		schedule.nextRecovery, schedule.retryDelay = time.Time{}, 0
	}
	if err != nil {
		schedule.recoveryAttempt(err)
		if m.forwardMutationPending(vm) {
			err = &forwardRecoveryFailure{outcome: "degraded", err: errors.Join(errForwardRecoveryRequired, err)}
		}
		return runtimeWatchContinue, err
	}
	if observed.State == process.RecordChanged {
		return runtimeWatchDone, nil
	}
	ctx, cancel := context.WithTimeout(ctx, runtimeAttemptTimeout)
	defer cancel()
	if observed.State == process.RecordExited {
		if now.Before(schedule.nextRecovery) {
			return runtimeWatchSkipped, nil
		}
		pending := m.forwardMutationPending(vm)
		err := m.store.WithGlobalVMContext(ctx, vm.Name, id, func() error { return nil })
		if err == nil {
			_, err = m.cleanupConfirmedExitLocked(ctx, vm)
		}
		schedule.recoveryAttempt(err)
		if err != nil {
			if pending {
				err = &forwardRecoveryFailure{outcome: "cleanup-after-exit", err: errors.Join(errForwardRecoveryRequired, err)}
			}
			return runtimeWatchContinue, err
		}
		return runtimeWatchDone, nil
	}
	if m.forwardMutationPending(vm) {
		if now.Before(schedule.nextRecovery) {
			return runtimeWatchSkipped, nil
		}
		_, err := m.recoverForwardMutation(ctx, vm, errForwardRecoveryRequired)
		schedule.recoveryAttempt(err)
		return runtimeWatchContinue, err
	}
	schedule.nextRecovery, schedule.retryDelay = time.Time{}, 0
	if now.Before(schedule.nextDiscovery) {
		return runtimeWatchSkipped, nil
	}
	schedule.nextDiscovery = schedule.nextDiscovery.Add(runtimeDiscoveryInterval)
	if !now.Before(schedule.nextDiscovery) {
		schedule.nextDiscovery = now.Add(runtimeDiscoveryInterval)
	}

	if !vm.Network.AutoForward || vm.MacOS != nil {
		return runtimeWatchContinue, m.cleanupAutoForwardsLocked(ctx, vm)
	}
	image, err := m.store.LoadImageByID(vm.Image.ID)
	if err != nil {
		return runtimeWatchContinue, err
	}
	if !image.Capabilities.GuestPortReport || !image.Capabilities.ControlShare {
		return runtimeWatchContinue, m.cleanupAutoForwardsLocked(ctx, vm)
	}
	_, err = m.reconcileAutoForwardsLocked(ctx, vm)
	return runtimeWatchContinue, err
}

func (m *Manager) logForwardWatchResult(vm *state.VMRecord, iterationErr error, lastErr string) string {
	if iterationErr != nil {
		msg := iterationErr.Error()
		if msg != lastErr {
			var failure *forwardRecoveryFailure
			if errors.As(iterationErr, &failure) {
				m.recordForwardRecovery(vm, failure.outcome, iterationErr)
			} else {
				m.appendAutoForwardLog(vm, "error", msg)
			}
		}
		return msg
	}
	if lastErr != "" {
		m.appendAutoForwardLog(vm, "info", "reconciliation recovered")
	}
	return ""
}
