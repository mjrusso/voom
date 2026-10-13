package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

// AutoForwardUpdate contains the auto-forward settings to change.
type AutoForwardUpdate struct {
	Enabled *bool
	Offset  *int
	Bind    *string
}

// UpdateAutoForward updates the selected auto-forward settings for a VM.
func (m *Manager) UpdateAutoForward(ctx context.Context, name string, update AutoForwardUpdate) (*state.VMRecord, error) {
	if update.Offset != nil && *update.Offset < 0 {
		return nil, errors.New("offset must be a non-negative integer")
	}
	if update.Bind != nil {
		bind, err := forward.NormalizeBind(*update.Bind)
		if err != nil {
			return nil, err
		}
		update.Bind = &bind
	}
	vm, unlock, err := m.lockForwardVM(ctx, name)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if update.Enabled != nil {
		vm.Network.AutoForward = *update.Enabled
	}
	if update.Offset != nil {
		vm.Network.AutoForwardHostOffset = *update.Offset
	}
	if update.Bind != nil {
		vm.Network.AutoForwardBind = *update.Bind
	}
	im, err := m.store.LoadImageByID(vm.Image.ID)
	if err != nil {
		return nil, err
	}
	if vm.Network.AutoForward {
		if err := RequireGuestPortReport(im, "auto-forwarding"); err != nil {
			return nil, err
		}
	}
	vm.UpdatedAt = time.Now().UTC()
	if err := m.store.SaveVM(vm); err != nil {
		return nil, err
	}
	if m.IsRunning(vm) {
		if vm.Network.AutoForward {
			_, removeErr := m.removeMismatchedAutoForwardsLocked(vm)
			_, removeErr = m.recoverLiveForwardMutation(vm, removeErr)
			if !m.IsRunning(vm) {
				return vm, removeErr
			}
			watchErr := m.StartAutoForwardWatcher(vm)
			if err := errors.Join(removeErr, watchErr); err != nil {
				return vm, err
			}
		} else {
			cleanupErr := m.cleanupAutoForwardsLocked(vm)
			if _, err := m.recoverLiveForwardMutation(vm, cleanupErr); err != nil {
				return vm, err
			}
			if err := m.StopAutoForwardWatcher(vm); err != nil {
				return vm, err
			}
		}
	}
	return vm, nil
}

// RuntimeAutoForwardsPath returns the path to the VM's persisted runtime
// auto-forward state file.
func (m *Manager) RuntimeAutoForwardsPath(vm *state.VMRecord) string {
	return m.store.Runtime(vm).AutoForwardsJSON()
}

// ReadRuntimeAutoForwards loads the saved reservations and last confirmed backend state.
func (m *Manager) ReadRuntimeAutoForwards(vm *state.VMRecord) ([]RuntimeAutoForward, error) {
	return forward.ReadRuntimeState(m.RuntimeAutoForwardsPath(vm))
}

func (m *Manager) saveRuntimeAutoForwards(vm *state.VMRecord, rows []RuntimeAutoForward) error {
	return forward.WriteRuntimeState(m.RuntimeAutoForwardsPath(vm), rows)
}

// StartAutoForwardWatcher spawns a detached `voom forward auto watch` process
// for the VM if one is not already running.
func (m *Manager) StartAutoForwardWatcher(vm *state.VMRecord) error {
	recordPath := m.store.Runtime(vm).AutoForwardProcessRecord()
	if _, ok := process.ValidRecord(recordPath, "auto-forward"); ok {
		return nil
	}
	if process.HasRecord(recordPath) {
		if err := process.StopRecorded(recordPath, "auto-forward", processStopTimeout); err != nil {
			return err
		}
	}
	return m.startSelfRecorded([]string{"forward", "auto", "watch", vm.ID}, m.store.LogPath(vm, "auto-forward"), recordPath)
}

// StopAutoForwardWatcher terminates the VM's auto-forward watcher process.
// A calling watcher exits through its loop after runtime cleanup.
func (m *Manager) StopAutoForwardWatcher(vm *state.VMRecord) error {
	record := m.store.Runtime(vm).AutoForwardProcessRecord()
	if pid, ok := process.ValidRecord(record, "auto-forward"); ok && pid == os.Getpid() {
		return nil
	}
	return process.StopRecorded(record, "auto-forward", processStopTimeout)
}

type autoForwardWatchOutcome uint8

const (
	autoForwardWatchSkipped autoForwardWatchOutcome = iota
	autoForwardWatchContinue
	autoForwardWatchDone
)

// WatchAutoForwards runs the foreground auto-forward reconciliation loop for a VM ID.
func (m *Manager) WatchAutoForwards(ctx context.Context, id string) error {
	vm, err := m.store.LoadVMByID(id)
	if err != nil {
		return err
	}
	defer func() { _ = process.RemoveRecord(m.store.Runtime(vm).AutoForwardProcessRecord()) }()
	ticker := time.NewTicker(autoForwardTick)
	defer ticker.Stop()
	lastErr := ""
	for {
		outcome, iterationErr := m.watchAutoForwardsOnce(id)
		lastErr = m.logAutoForwardWatchResult(vm, outcome, iterationErr, lastErr)
		if outcome == autoForwardWatchDone {
			return iterationErr
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (m *Manager) logAutoForwardWatchResult(vm *state.VMRecord, outcome autoForwardWatchOutcome, iterationErr error, lastErr string) string {
	if outcome == autoForwardWatchSkipped {
		return lastErr
	}
	if iterationErr != nil {
		msg := iterationErr.Error()
		if msg != lastErr {
			m.appendAutoForwardLog(vm, "error", msg)
		}
		return msg
	}
	if lastErr != "" {
		m.appendAutoForwardLog(vm, "info", "reconciliation recovered")
	}
	return ""
}

func (m *Manager) watchAutoForwardsOnce(id string) (autoForwardWatchOutcome, error) {
	unlock, err := m.store.TryLockVM(id)
	if err != nil {
		return autoForwardWatchContinue, err
	}
	if unlock == nil {
		return autoForwardWatchSkipped, nil
	}
	defer unlock()
	vm, err := m.store.LoadVMByID(id)
	if errors.Is(err, os.ErrNotExist) {
		return autoForwardWatchDone, nil
	}
	if err != nil {
		return autoForwardWatchContinue, err
	}
	return m.watchAutoForwardsLocked(vm)
}

// The caller must hold the VM lock.
func (m *Manager) watchAutoForwardsLocked(vm *state.VMRecord) (autoForwardWatchOutcome, error) {
	if err := m.pendingForwardMutationError(vm); err != nil {
		return m.autoForwardWatchAfterMutation(vm, err)
	}
	if !m.IsRunning(vm) || !vm.Network.AutoForward {
		return m.finishAutoForwardWatch(vm)
	}
	im, err := m.store.LoadImageByID(vm.Image.ID)
	if err != nil {
		return autoForwardWatchContinue, err
	}
	if !im.Capabilities.GuestPortReport || !im.Capabilities.ControlShare {
		return m.finishAutoForwardWatch(vm)
	}
	_, err = m.reconcileAutoForwardsLocked(vm)
	return m.autoForwardWatchAfterMutation(vm, err)
}

func (m *Manager) autoForwardWatchAfterMutation(vm *state.VMRecord, err error) (autoForwardWatchOutcome, error) {
	_, err = m.recoverLiveForwardMutation(vm, err)
	if !m.IsRunning(vm) && !m.forwardMutationPending(vm) {
		return autoForwardWatchDone, err
	}
	return autoForwardWatchContinue, err
}

func (m *Manager) finishAutoForwardWatch(vm *state.VMRecord) (autoForwardWatchOutcome, error) {
	var err error
	if m.gvproxyMayBeRunning(vm) {
		err = m.cleanupAutoForwardsLocked(vm)
	} else {
		err = m.clearRuntimeAutoForwardsLocked(vm)
	}
	if errors.Is(err, errForwardRecoveryRequired) {
		return m.autoForwardWatchAfterMutation(vm, err)
	}
	if err != nil {
		return autoForwardWatchContinue, err
	}
	return autoForwardWatchDone, nil
}

// The caller must hold the VM lock.
func (m *Manager) cleanupAutoForwardsLocked(vm *state.VMRecord) error {
	oldRows, err := m.ReadRuntimeAutoForwards(vm)
	if err != nil {
		return err
	}
	_, err = m.removeAutoForwardRowsLocked(vm, oldRows, func(RuntimeAutoForward) bool { return true })
	return err
}

// The caller must hold the VM lock and confirm gvproxy termination.
func (m *Manager) clearRuntimeAutoForwardsLocked(vm *state.VMRecord) error {
	oldRows, err := m.ReadRuntimeAutoForwards(vm)
	if err != nil {
		return err
	}
	if err := m.saveRuntimeAutoForwards(vm, nil); err != nil {
		return err
	}
	m.emitAutoForwardTransitions(vm, oldRows, nil)
	return nil
}

// ReconcileAutoForwards reconciles a running VM under its per-VM lock.
func (m *Manager) ReconcileAutoForwards(ctx context.Context, name string) ([]RuntimeAutoForward, error) {
	vm, unlock, err := m.lockForwardVM(ctx, name)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if !m.IsRunning(vm) {
		return nil, fmt.Errorf("VM %q is not running", vm.Name)
	}
	im, err := m.store.LoadImageByID(vm.Image.ID)
	if err != nil {
		return nil, err
	}
	if err := RequireGuestPortReport(im, "auto-forward reconciliation"); err != nil {
		return nil, err
	}
	rows, err := m.reconcileAutoForwardsLocked(vm)
	if errors.Is(err, errForwardRecoveryRequired) {
		_, err = m.recoverLiveForwardMutation(vm, err)
		var readErr error
		rows, readErr = m.ReadRuntimeAutoForwards(vm)
		err = errors.Join(err, readErr)
	}
	return rows, err
}

// The caller must hold the VM lock.
func (m *Manager) reconcileAutoForwardsLocked(vm *state.VMRecord) ([]RuntimeAutoForward, error) {
	if err := ValidateAutoForwardConfig(vm); err != nil {
		return nil, err
	}
	oldRows, err := m.removeMismatchedAutoForwardsLocked(vm)
	if err != nil {
		return oldRows, err
	}
	if !vm.Network.AutoForward {
		return m.removeAutoForwardRowsLocked(vm, oldRows, func(RuntimeAutoForward) bool { return true })
	}
	report, err := m.ReadGuestPorts(vm)
	if err != nil {
		return oldRows, err
	}
	desired, err := m.PlanAutoForwards(vm, *report, oldRows)
	if err != nil {
		return oldRows, err
	}
	desiredEndpoints := map[forward.Decl]struct{}{}
	for i := range desired {
		if desired[i].Reason == "" {
			desiredEndpoints[desired[i].Endpoint()] = struct{}{}
		}
	}
	oldRows, err = m.removeAutoForwardRowsLocked(vm, oldRows, func(row RuntimeAutoForward) bool {
		_, keep := desiredEndpoints[row.Endpoint()]
		return row.Installed() && !keep
	})
	if err != nil {
		return oldRows, err
	}
	rows, err := m.installAutoForwardRowsLocked(vm, desired, oldRows)
	if err != nil {
		return rows, err
	}
	m.emitAutoForwardTransitions(vm, oldRows, rows)
	return rows, nil
}

// The caller must hold the VM lock.
func (m *Manager) installAutoForwardRowsLocked(vm *state.VMRecord, plans []forward.AutoPlan, oldRows []RuntimeAutoForward) ([]RuntimeAutoForward, error) {
	if err := m.pendingForwardMutationError(vm); err != nil {
		rows, readErr := m.ReadRuntimeAutoForwards(vm)
		return rows, errors.Join(err, readErr)
	}
	gvproxyPID := m.runtimeGVProxyPID(vm)
	oldEndpoints := map[forward.Decl]struct{}{}
	for _, old := range oldRows {
		if old.Installed() && old.GVProxyPID == gvproxyPID {
			oldEndpoints[old.Endpoint()] = struct{}{}
		}
	}
	var operations []forwardOperation
	for _, plan := range plans {
		if plan.Reason != "" {
			continue
		}
		endpoint := plan.Endpoint()
		if _, installed := oldEndpoints[endpoint]; !installed {
			operations = append(operations, forwardOperation{endpoint: endpoint, guestTargetIP: plan.GuestTargetIP})
		}
	}
	var rows []RuntimeAutoForward
	err := m.executeForwardMutation(vm, forwardMutation{
		rejection:  forwardSkipRejected,
		operations: operations,
		commit: func(outcomes []forwardOutcome) error {
			byEndpoint := make(map[forward.Decl]forwardOutcome, len(outcomes))
			for _, outcome := range outcomes {
				byEndpoint[outcome.operation.endpoint] = outcome
			}
			rows = make([]RuntimeAutoForward, len(plans))
			for i, plan := range plans {
				reason := plan.Reason
				if _, retained := oldEndpoints[plan.Endpoint()]; reason == "" && !retained {
					outcome, exists := byEndpoint[plan.Endpoint()]
					if !exists {
						return fmt.Errorf("missing forward outcome for %s", forward.Address(plan.Bind, plan.HostPort))
					}
					if outcome.err != nil {
						reason = outcome.err.Error()
						if !errors.Is(outcome.err, errForwardPortReserved) {
							reason = "gvproxy expose failed: " + reason
						}
					}
				}
				status, pid := forward.ReservationSkipped, 0
				if reason == "" {
					status, pid = forward.ReservationActive, gvproxyPID
				}
				rows[i] = RuntimeAutoForward{
					Protocol: plan.Protocol, Bind: plan.Bind, HostPort: plan.HostPort, GuestPort: plan.GuestPort,
					GuestTargetIP: plan.GuestTargetIP, Offset: plan.Offset, Status: status, Reason: reason, GVProxyPID: pid,
				}
			}
			return m.saveRuntimeAutoForwards(vm, rows)
		},
	})
	if err != nil {
		persisted, readErr := m.ReadRuntimeAutoForwards(vm)
		return persisted, errors.Join(err, readErr)
	}
	return rows, nil
}

// The caller must hold the VM lock.
func (m *Manager) removeMismatchedAutoForwardsLocked(vm *state.VMRecord) ([]RuntimeAutoForward, error) {
	oldRows, err := m.ReadRuntimeAutoForwards(vm)
	if err != nil {
		return nil, err
	}
	bind := autoForwardBind(vm)
	offset := vm.Network.AutoForwardHostOffset
	return m.removeAutoForwardRowsLocked(vm, oldRows, func(row RuntimeAutoForward) bool {
		return row.Bind != bind || row.Offset != offset
	})
}

// The caller must hold the VM lock.
func (m *Manager) removeAutoForwardRowsLocked(vm *state.VMRecord, oldRows []RuntimeAutoForward, remove func(RuntimeAutoForward) bool) ([]RuntimeAutoForward, error) {
	if err := m.pendingForwardMutationError(vm); err != nil {
		rows, readErr := m.ReadRuntimeAutoForwards(vm)
		return rows, errors.Join(err, readErr)
	}
	remaining := make([]RuntimeAutoForward, 0, len(oldRows))
	var operations []forwardOperation
	for _, row := range oldRows {
		if !remove(row) {
			remaining = append(remaining, row)
		} else if row.Installed() {
			operations = append(operations, forwardOperation{
				action:   forwardUnexpose,
				endpoint: row.Endpoint(),
			})
		}
	}
	if len(oldRows) > 0 && len(remaining) == len(oldRows) {
		return oldRows, nil
	}
	var err error
	if len(operations) == 0 {
		err = m.saveRuntimeAutoForwards(vm, remaining)
	} else {
		err = m.executeForwardMutation(vm, forwardMutation{
			operations: operations,
			commit:     func([]forwardOutcome) error { return m.saveRuntimeAutoForwards(vm, remaining) },
		})
	}
	if err != nil {
		rows, readErr := m.ReadRuntimeAutoForwards(vm)
		return rows, errors.Join(err, readErr)
	}
	m.emitAutoForwardTransitions(vm, oldRows, remaining)
	return remaining, nil
}

func (m *Manager) gvproxyMayBeRunning(vm *state.VMRecord) bool {
	rt := m.store.Runtime(vm)
	return m.runtimeGVProxyPID(vm) != 0 || process.HasRecord(rt.GVProxyProcessRecord()) || socketExists(rt.NetworkSock())
}

func (m *Manager) emitAutoForwardTransitions(vm *state.VMRecord, oldRows, newRows []RuntimeAutoForward) {
	oldByEndpoint := make(map[forward.Decl]RuntimeAutoForward, len(oldRows))
	newByEndpoint := make(map[forward.Decl]RuntimeAutoForward, len(newRows))
	for _, row := range oldRows {
		oldByEndpoint[row.Endpoint()] = row
	}
	for _, row := range newRows {
		newByEndpoint[row.Endpoint()] = row
	}
	for endpoint, old := range oldByEndpoint {
		if next, ok := newByEndpoint[endpoint]; old.Installed() && (!ok || !next.Installed()) {
			m.emitForward("uninstall", vm, old)
		}
	}
	for endpoint, next := range newByEndpoint {
		old, existed := oldByEndpoint[endpoint]
		if next.Installed() && (!existed || !old.Installed()) {
			m.emitForward("install", vm, next)
		}
		if next.Status == forward.ReservationSkipped && (!existed || old.Status != forward.ReservationSkipped || old.Reason != next.Reason) {
			m.emitForward("skip", vm, next)
		}
	}
}

func (m *Manager) appendAutoForwardLog(vm *state.VMRecord, level, msg string) {
	path := m.store.LogPath(vm, "auto-forward")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	log, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = log.Close() }()
	_, _ = fmt.Fprintf(log, "%s %s: %s\n", time.Now().UTC().Format(time.RFC3339), level, msg)
}

// PlanAutoForwards computes the desired auto-forward rows for the VM given a
// guest report and the existing runtime state.
func (m *Manager) PlanAutoForwards(vm *state.VMRecord, report GuestPortsReport, existing []RuntimeAutoForward) ([]forward.AutoPlan, error) {
	var reserved state.PortReservations
	err := m.store.WithGlobal(func() error {
		var err error
		reserved, err = m.store.PortReservations(vm.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return forward.PlanAuto(forward.VMPlanConfig{
		HostOffset:    vm.Network.AutoForwardHostOffset,
		HostBind:      autoForwardBind(vm),
		GuestTargetIP: m.GuestTargetIP(vm),
	}, report, existing, forward.PlanOptions{
		PortReserved: func(bind string, port int) (bool, error) {
			return reserved.Reserved("tcp", bind, port), nil
		},
		HostPortAvailable: forward.HostPortAvailable,
	})
}

// ValidateAutoForwardConfig checks the VM's auto-forward configuration for
// sane values before reconciliation.
func ValidateAutoForwardConfig(vm *state.VMRecord) error {
	if vm.Network.AutoForwardHostOffset < 0 {
		return errors.New("auto-forward offset must be a non-negative integer")
	}
	if _, err := forward.NormalizeBind(autoForwardBind(vm)); err != nil {
		return fmt.Errorf("auto-forward bind: %w", err)
	}
	return nil
}

func autoForwardBind(vm *state.VMRecord) string {
	if vm.Network.AutoForwardBind == "" {
		return "127.0.0.1"
	}
	return vm.Network.AutoForwardBind
}

func (m *Manager) runtimeGVProxyPID(vm *state.VMRecord) int {
	pid, ok := process.ValidRecord(m.store.Runtime(vm).GVProxyProcessRecord(), "gvproxy")
	if !ok {
		return 0
	}
	return pid
}

func allocateAutoPort(reserved state.PortReservations, protocol, bind string) (int, error) {
	for p := autoForwardPortLow; p <= autoForwardPortHigh; p++ {
		if !reserved.Reserved(protocol, bind, p) && !portBusy(protocol, bind, p) {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free auto-forward port in %d-%d", autoForwardPortLow, autoForwardPortHigh)
}
