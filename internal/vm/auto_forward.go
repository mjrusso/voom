package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

// SetAutoForward toggles auto-forwarding on the VM and optionally updates the
// host port offset and bind address, reconciling and starting/stopping the
// watcher as needed.
func (m *Manager) SetAutoForward(ctx context.Context, name string, enabled bool, offset int, setOffset bool, bind string, setBind bool) (*state.VMRecord, error) {
	if setOffset && offset < 0 {
		return nil, errors.New("offset must be a non-negative integer")
	}
	if setBind {
		var err error
		bind, err = forward.NormalizeBind(bind)
		if err != nil {
			return nil, err
		}
	}
	vm, err := m.store.LoadVM(name)
	if err != nil {
		return nil, err
	}
	unlock, err := m.store.LockVM(vm.ID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	vm, err = m.store.LoadVM(vm.Name)
	if err != nil {
		return nil, err
	}
	vm.Network.AutoForward = enabled
	if setOffset {
		vm.Network.AutoForwardHostOffset = offset
	}
	if setBind {
		vm.Network.AutoForwardBind = bind
	}
	im, err := m.store.LoadImageByID(vm.Image.ID)
	if err != nil {
		return nil, err
	}
	if enabled {
		if err := RequireGuestPortReport(im, "auto-forwarding"); err != nil {
			return nil, err
		}
	}
	vm.UpdatedAt = time.Now().UTC()
	if err := m.store.SaveVM(vm); err != nil {
		return nil, err
	}
	if m.IsRunning(vm) {
		if enabled {
			if _, err := m.ReconcileAutoForwards(ctx, vm); err != nil {
				return nil, err
			}
			if err := m.StartAutoForwardWatcher(vm); err != nil {
				return nil, err
			}
		} else {
			m.StopAutoForwardWatcher(vm)
			if err := m.CleanupAutoForwards(ctx, vm); err != nil {
				return nil, err
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

// AutoForwardWatcherPidfile returns the path to the pidfile for the VM's
// auto-forward watcher process.
func (m *Manager) AutoForwardWatcherPidfile(vm *state.VMRecord) string {
	return m.store.Runtime(vm).AutoForwardPid()
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
	pidfile := m.AutoForwardWatcherPidfile(vm)
	if pid, ok := validPid(pidfile, "auto-forward"); ok && processAlive(pid) {
		return nil
	}
	_ = os.Remove(pidfile)
	cmd, err := m.startSelfDetached([]string{"forward", "auto", "watch", vm.Name}, m.store.LogPath(vm, "auto-forward"))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(pidfile), 0o755); err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	return os.WriteFile(pidfile, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644)
}

// StopAutoForwardWatcher terminates the VM's auto-forward watcher process.
func (m *Manager) StopAutoForwardWatcher(vm *state.VMRecord) {
	pidfile := m.AutoForwardWatcherPidfile(vm)
	process.StopPidfile(pidfile, "auto-forward", processStopTimeout)
}

// WatchAutoForwards runs the foreground auto-forward reconciliation loop for
// the VM, exiting when ctx is cancelled, the VM stops, or auto-forwarding is disabled.
func (m *Manager) WatchAutoForwards(ctx context.Context, name string) error {
	vm, err := m.store.LoadVM(name)
	if err != nil {
		return err
	}
	pidfile := m.AutoForwardWatcherPidfile(vm)
	if err := os.MkdirAll(filepath.Dir(pidfile), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		return err
	}
	defer func() { _ = os.Remove(pidfile) }()
	ticker := time.NewTicker(autoForwardTick)
	defer ticker.Stop()
	for {
		vm, err := m.store.LoadVM(name)
		if err != nil {
			return err
		}
		if !m.IsRunning(vm) {
			_ = m.CleanupAutoForwards(ctx, vm)
			return nil
		}
		im, err := m.store.LoadImageByID(vm.Image.ID)
		if err != nil {
			return err
		}
		if !vm.Network.AutoForward || !im.Capabilities.GuestPortReport || !im.Capabilities.ControlShare {
			_ = m.CleanupAutoForwards(ctx, vm)
			return nil
		}
		if _, err := m.ReconcileAutoForwards(ctx, vm); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "auto-forward reconcile for %s failed: %v\n", name, err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// CleanupAutoForwards unexposes any installed auto-forwards from gvproxy and
// clears the persisted runtime state.
func (m *Manager) CleanupAutoForwards(_ context.Context, vm *state.VMRecord) error {
	oldRows, err := m.ReadRuntimeAutoForwards(vm)
	if err != nil {
		return err
	}
	sock := m.store.Runtime(vm).NetworkSock()
	for _, old := range oldRows {
		if old.Installed {
			_ = gvproxyUnexpose(sock, fmt.Sprintf("%s:%d", old.Bind, old.HostPort))
		}
	}
	return m.saveRuntimeAutoForwards(vm, nil)
}

// ReconcileAutoForwards computes the desired auto-forward set from the guest
// port report and applies the diff against gvproxy, returning the new rows.
func (m *Manager) ReconcileAutoForwards(_ context.Context, vm *state.VMRecord) ([]RuntimeAutoForward, error) {
	oldRows, err := m.ReadRuntimeAutoForwards(vm)
	if err != nil {
		return nil, err
	}
	if err := ValidateAutoForwardConfig(vm); err != nil {
		return nil, err
	}
	var report *GuestPortsReport
	if vm.Network.AutoForward {
		var reportErr error
		report, reportErr = m.ReadGuestPorts(vm)
		if reportErr != nil {
			m.LogAutoForwardWarning(vm, "guest report unavailable: "+reportErr.Error())
		}
	}
	desired := m.PlanAutoForwards(vm, report, oldRows)
	gvproxyPID := m.runtimeGVProxyPID(vm)
	desiredKeys := map[string]struct{}{}
	for i := range desired {
		if desired[i].Installed {
			desiredKeys[forward.Key(desired[i])] = struct{}{}
		}
	}
	sock := m.store.Runtime(vm).NetworkSock()
	for _, old := range oldRows {
		if _, keep := desiredKeys[forward.Key(old)]; !old.Installed || keep {
			continue
		}
		_ = gvproxyUnexpose(sock, fmt.Sprintf("%s:%d", old.Bind, old.HostPort))
	}
	oldKeys := map[string]struct{}{}
	for _, old := range oldRows {
		if old.Installed && old.GVProxyPID == gvproxyPID {
			oldKeys[forward.Key(old)] = struct{}{}
		}
	}
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
		}
	}
	if err := m.saveRuntimeAutoForwards(vm, desired); err != nil {
		return nil, err
	}
	return desired, nil
}

// LogAutoForwardWarning appends a timestamped warning to the VM's auto-forward log.
func (m *Manager) LogAutoForwardWarning(vm *state.VMRecord, msg string) {
	path := m.store.LogPath(vm, "auto-forward")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	log, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = log.Close() }()
	_, _ = fmt.Fprintf(log, "%s warning: %s\n", time.Now().UTC().Format(time.RFC3339), msg)
}

// PlanAutoForwards computes the desired auto-forward rows for the VM given a
// guest report and the existing runtime state.
func (m *Manager) PlanAutoForwards(vm *state.VMRecord, report *GuestPortsReport, existing []RuntimeAutoForward) []RuntimeAutoForward {
	return forward.PlanAuto(forward.VMPlanConfig{
		VMID:          vm.ID,
		AutoForward:   vm.Network.AutoForward,
		HostOffset:    vm.Network.AutoForwardHostOffset,
		HostBind:      autoForwardBind(vm),
		GuestTargetIP: m.GuestTargetIP(vm),
	}, report, existing, forward.PlanOptions{
		PortReserved: func(bind string, port int) bool {
			reserved, err := m.store.PortReserved(bind, port)
			if err != nil {
				m.LogAutoForwardWarning(vm, "could not inspect declared port reservations: "+err.Error())
				return true
			}
			return reserved
		},
		RuntimeAutoReserved: func(bind string, port int) bool { return m.runtimeAutoReserved(vm.ID, bind, port) },
		HostPortAvailable:   forward.HostPortAvailable,
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
	pid, ok := validPid(m.store.Runtime(vm).GVProxyPid(), "gvproxy")
	if !ok {
		return 0
	}
	return pid
}

func (m *Manager) runtimeAutoReserved(exceptVMID, bind string, port int) bool {
	vms, err := m.store.ListVMs()
	if err != nil {
		return true
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
				return true
			}
		}
	}
	return false
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
