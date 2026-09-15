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
	vm, unlock, err := m.store.LockVMRecord(ctx, name)
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
			watchErr := m.StartAutoForwardWatcher(vm)
			if err := errors.Join(removeErr, watchErr); err != nil {
				return vm, err
			}
		} else {
			if err := m.cleanupAutoForwardsLocked(vm); err != nil {
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

// ReadRuntimeAutoForwards loads the persisted auto-forward runtime rows for the VM.
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
func (m *Manager) StopAutoForwardWatcher(vm *state.VMRecord) error {
	return process.StopRecorded(m.store.Runtime(vm).AutoForwardProcessRecord(), "auto-forward", processStopTimeout)
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
	if !m.IsRunning(vm) || !vm.Network.AutoForward {
		if err := m.cleanupAutoForwardsLocked(vm); err != nil {
			return autoForwardWatchContinue, err
		}
		return autoForwardWatchDone, nil
	}
	im, err := m.store.LoadImageByID(vm.Image.ID)
	if err != nil {
		return autoForwardWatchContinue, err
	}
	if !im.Capabilities.GuestPortReport || !im.Capabilities.ControlShare {
		if err := m.cleanupAutoForwardsLocked(vm); err != nil {
			return autoForwardWatchContinue, err
		}
		return autoForwardWatchDone, nil
	}
	_, err = m.reconcileAutoForwardsLocked(vm)
	return autoForwardWatchContinue, err
}

// The caller must hold the VM lock.
func (m *Manager) cleanupAutoForwardsLocked(vm *state.VMRecord) error {
	oldRows, err := m.ReadRuntimeAutoForwards(vm)
	if err != nil {
		return err
	}
	if len(oldRows) == 0 {
		return m.saveRuntimeAutoForwards(vm, nil)
	}
	_, err = m.removeAutoForwardRowsLocked(vm, oldRows, func(RuntimeAutoForward) bool { return true })
	return err
}

// ReconcileAutoForwards reconciles a running VM under its per-VM lock.
func (m *Manager) ReconcileAutoForwards(ctx context.Context, name string) ([]RuntimeAutoForward, error) {
	vm, unlock, err := m.store.LockVMRecord(ctx, name)
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
	return m.reconcileAutoForwardsLocked(vm)
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
		remaining, err := m.removeAutoForwardRowsLocked(vm, oldRows, func(RuntimeAutoForward) bool { return true })
		return remaining, err
	}
	report, err := m.ReadGuestPorts(vm)
	if err != nil {
		return oldRows, err
	}
	desired, err := m.PlanAutoForwards(vm, *report, oldRows)
	if err != nil {
		return oldRows, err
	}
	gvproxyPID := m.runtimeGVProxyPID(vm)
	desiredKeys := map[string]struct{}{}
	for i := range desired {
		if desired[i].Installed {
			desiredKeys[forward.Key(desired[i])] = struct{}{}
		}
	}
	oldRows, err = m.removeAutoForwardRowsLocked(vm, oldRows, func(row RuntimeAutoForward) bool {
		_, keep := desiredKeys[forward.Key(row)]
		return row.Installed && !keep
	})
	if err != nil {
		return oldRows, err
	}
	oldKeys := map[string]struct{}{}
	for _, old := range oldRows {
		if old.Installed && old.GVProxyPID == gvproxyPID {
			oldKeys[forward.Key(old)] = struct{}{}
		}
	}
	sock := m.store.Runtime(vm).NetworkSock()
	newlyExposed := []RuntimeAutoForward{}
	for i := range desired {
		if !desired[i].Installed {
			continue
		}
		if _, ok := oldKeys[forward.Key(desired[i])]; ok {
			desired[i].GVProxyPID = gvproxyPID
			continue
		}
		err := gvproxyExpose(sock, fmt.Sprintf("%s:%d", desired[i].Bind, desired[i].HostPort), fmt.Sprintf("%s:%d", desired[i].GuestTargetIP, desired[i].GuestPort))
		if err != nil {
			desired[i].Installed = false
			desired[i].Status = "skipped"
			desired[i].Reason = "gvproxy expose failed: " + err.Error()
			desired[i].GVProxyPID = 0
		} else {
			desired[i].GVProxyPID = gvproxyPID
			newlyExposed = append(newlyExposed, desired[i])
		}
	}
	if err := m.saveRuntimeAutoForwards(vm, desired); err != nil {
		failures := []error{err}
		for _, row := range newlyExposed {
			if rollbackErr := gvproxyUnexpose(sock, fmt.Sprintf("%s:%d", row.Bind, row.HostPort)); rollbackErr != nil {
				failures = append(failures, fmt.Errorf("roll back auto-forward %s:%d: %w", row.Bind, row.HostPort, rollbackErr))
			}
		}
		return oldRows, errors.Join(failures...)
	}
	m.emitAutoForwardTransitions(vm, oldRows, desired)
	return desired, nil
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
	rt := m.store.Runtime(vm)
	gvproxyMayBeRunning := m.runtimeGVProxyPID(vm) != 0 || process.HasRecord(rt.GVProxyProcessRecord()) || socketExists(rt.NetworkSock())
	remaining := make([]RuntimeAutoForward, 0, len(oldRows))
	unexposed := []RuntimeAutoForward{}
	removed := false
	var failures []error
	for _, row := range oldRows {
		if !remove(row) {
			remaining = append(remaining, row)
			continue
		}
		if row.Installed && gvproxyMayBeRunning {
			if err := gvproxyUnexpose(rt.NetworkSock(), fmt.Sprintf("%s:%d", row.Bind, row.HostPort)); err != nil {
				remaining = append(remaining, row)
				failures = append(failures, fmt.Errorf("unexpose auto-forward %s:%d: %w", row.Bind, row.HostPort, err))
				continue
			}
			unexposed = append(unexposed, row)
		}
		removed = true
	}
	if !removed {
		return oldRows, errors.Join(failures...)
	}
	if err := m.saveRuntimeAutoForwards(vm, remaining); err != nil {
		failures = append(failures, err)
		for _, row := range unexposed {
			local := fmt.Sprintf("%s:%d", row.Bind, row.HostPort)
			remote := fmt.Sprintf("%s:%d", row.GuestTargetIP, row.GuestPort)
			if rollbackErr := gvproxyExpose(rt.NetworkSock(), local, remote); rollbackErr != nil {
				failures = append(failures, fmt.Errorf("roll back auto-forward removal %s: %w", local, rollbackErr))
			}
		}
		return oldRows, errors.Join(failures...)
	}
	m.emitAutoForwardTransitions(vm, oldRows, remaining)
	return remaining, errors.Join(failures...)
}

func (m *Manager) emitAutoForwardTransitions(vm *state.VMRecord, oldRows, newRows []RuntimeAutoForward) {
	oldByKey := make(map[string]RuntimeAutoForward, len(oldRows))
	newByKey := make(map[string]RuntimeAutoForward, len(newRows))
	for _, row := range oldRows {
		oldByKey[forward.Key(row)] = row
	}
	for _, row := range newRows {
		newByKey[forward.Key(row)] = row
	}
	for key, old := range oldByKey {
		if next, ok := newByKey[key]; old.Installed && (!ok || !next.Installed) {
			m.emitForward("uninstall", vm, old)
		}
	}
	for key, next := range newByKey {
		old, existed := oldByKey[key]
		if next.Installed && (!existed || !old.Installed) {
			m.emitForward("install", vm, next)
		}
		if next.Status == "skipped" && (!existed || old.Status != "skipped" || old.Reason != next.Reason) {
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
func (m *Manager) PlanAutoForwards(vm *state.VMRecord, report GuestPortsReport, existing []RuntimeAutoForward) ([]RuntimeAutoForward, error) {
	return forward.PlanAuto(forward.VMPlanConfig{
		HostOffset:    vm.Network.AutoForwardHostOffset,
		HostBind:      autoForwardBind(vm),
		GuestTargetIP: m.GuestTargetIP(vm),
	}, report, existing, forward.PlanOptions{
		PortReserved: func(bind string, port int) (bool, error) {
			reserved, err := m.store.PortReserved(bind, port)
			if err != nil {
				return false, fmt.Errorf("inspect declared port reservations: %w", err)
			}
			return reserved, nil
		},
		RuntimeAutoReserved: func(bind string, port int) (bool, error) {
			reserved, err := m.runtimeAutoReserved(vm.ID, bind, port)
			if err != nil {
				return false, fmt.Errorf("inspect runtime auto-forward reservations: %w", err)
			}
			return reserved, nil
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

func (m *Manager) runtimeAutoReserved(exceptVMID, bind string, port int) (bool, error) {
	vms, err := m.store.ListVMs()
	if err != nil {
		return false, err
	}
	for _, vm := range vms {
		if vm.ID == exceptVMID {
			continue
		}
		rows, err := m.ReadRuntimeAutoForwards(vm)
		if err != nil {
			continue
		}
		for _, f := range rows {
			if f.Installed && f.HostPort == port && forward.BindsConflict(bind, f.Bind) {
				return true, nil
			}
		}
	}
	return false, nil
}

func (m *Manager) allocateAutoPort(bind string) (int, error) {
	for p := autoForwardPortLow; p <= autoForwardPortHigh; p++ {
		reserved, err := m.store.PortReserved(bind, p)
		if err != nil {
			return 0, err
		}
		if !reserved && !portBusy(bind, p) {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free auto-forward port in %d-%d", autoForwardPortLow, autoForwardPortHigh)
}
