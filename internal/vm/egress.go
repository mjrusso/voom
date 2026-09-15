package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/mjrusso/voom/internal/egress"
	"github.com/mjrusso/voom/internal/events"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

// EgressResult distinguishes declaration changes from runtime repairs.
type EgressResult struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	Changed        bool         `json:"changed"`
	RuntimeChanged bool         `json:"runtimeChanged"`
	Egress         *egress.Decl `json:"egress"`
}

// EgressOptions guards the target identity during an update.
type EgressOptions struct {
	ExpectedID string
}

// CheckEgressAssignment checks reservations; mutations must hold the global state lock.
func (m *Manager) CheckEgressAssignment(vm *state.VMRecord, d egress.Decl) error {
	records, err := m.store.ListVMs()
	if err != nil {
		return err
	}
	current, _ := os.Lstat(d.BackendSocket)
	for _, other := range records {
		if other.ID == vm.ID || other.Network.Egress == nil {
			continue
		}
		path := other.Network.Egress.BackendSocket
		info, _ := os.Lstat(path)
		if path == d.BackendSocket || (current != nil && info != nil && os.SameFile(current, info)) {
			return fmt.Errorf("backend socket is reserved by VM %s (%s)", other.Name, other.ID)
		}
	}
	return nil
}

func (m *Manager) validateEgressTarget(ctx context.Context, vm *state.VMRecord) ([]byte, error) {
	d := vm.Network.Egress
	if err := egress.Syntax(*d); err != nil {
		return nil, err
	}
	if err := m.CheckEgressImage(vm); err != nil {
		return nil, err
	}
	if err := egress.ProbeSocket(ctx, d.BackendSocket); err != nil {
		return nil, err
	}
	return egress.ReadCA(d.CACertPath)
}

// CheckEgressImage verifies that the VM image can publish egress metadata.
func (m *Manager) CheckEgressImage(vm *state.VMRecord) error {
	im, err := m.store.LoadImageByID(vm.Image.ID)
	if err != nil {
		return err
	}
	if !im.Capabilities.ControlShare {
		return errors.New("egress requires an image with controlShare capability")
	}
	return nil
}

// CheckEgressCapabilities verifies the required gvproxy APIs for the VM's current state.
func (m *Manager) CheckEgressCapabilities(ctx context.Context, vm *state.VMRecord) error {
	if m.IsRunning(vm) {
		rt := m.store.Runtime(vm)
		if _, ok := process.ValidRecord(rt.GVProxyProcessRecord(), "gvproxy"); !ok {
			return errors.New("VM gvproxy process identity is unavailable")
		}
		return gvproxy.CheckProcess(ctx, rt.NetworkSock(), gvproxy.GuestIsolationCapability, gvproxy.GatewayCapability)
	}
	_, err := gvproxy.ResolveCompatible(ctx, gvproxy.GuestIsolationCapability, gvproxy.GatewayCapability)
	return err
}

func (m *Manager) validateEgress(ctx context.Context, vm *state.VMRecord) ([]byte, error) {
	ca, err := m.validateEgressTarget(ctx, vm)
	if err != nil {
		return nil, err
	}
	if err := m.CheckEgressCapabilities(ctx, vm); err != nil {
		return nil, err
	}
	return ca, nil
}

// SetEgress replaces an attachment; a running VM may only change its enabled state.
func (m *Manager) SetEgress(ctx context.Context, name string, decl egress.Decl, opts EgressOptions) (EgressResult, error) {
	return m.updateEgress(ctx, name, "set", opts.ExpectedID, func(*egress.Decl) (*egress.Decl, error) {
		return &decl, nil
	})
}

// ClearEgress releases a stopped VM's attachment after runtime cleanup.
func (m *Manager) ClearEgress(ctx context.Context, name string, opts EgressOptions) (EgressResult, error) {
	return m.updateEgress(ctx, name, "clear", opts.ExpectedID, func(*egress.Decl) (*egress.Decl, error) {
		return nil, nil
	})
}

// EnableEgress validates and reconciles the attachment without restarting healthy runtime.
func (m *Manager) EnableEgress(ctx context.Context, name string, opts EgressOptions) (EgressResult, error) {
	return m.updateEgress(ctx, name, "enable", opts.ExpectedID, func(current *egress.Decl) (*egress.Decl, error) {
		if current == nil {
			return nil, errors.New("egress is not configured")
		}
		current.Enabled = true
		return current, nil
	})
}

// DisableEgress revokes attachment access; direct networking remains available.
func (m *Manager) DisableEgress(ctx context.Context, name string, opts EgressOptions) (EgressResult, error) {
	return m.updateEgress(ctx, name, "disable", opts.ExpectedID, func(current *egress.Decl) (*egress.Decl, error) {
		if current == nil {
			return nil, errors.New("egress is not configured")
		}
		current.Enabled = false
		return current, nil
	})
}

func (m *Manager) updateEgress(ctx context.Context, name, action, expectedID string, next func(*egress.Decl) (*egress.Decl, error)) (result EgressResult, retErr error) {
	vm, unlock, err := m.store.LockVMRecord(ctx, name)
	if err != nil {
		return result, err
	}
	defer unlock()
	result = newEgressResult(vm)
	defer func() { m.finishEgressResult(action, vm, &result, retErr) }()
	if err := checkExpectedVMID(vm, expectedID); err != nil {
		return result, err
	}
	current := copyEgressDecl(vm.Network.Egress)
	nextDecl, err := next(copyEgressDecl(current))
	if err != nil {
		return result, err
	}
	running := m.IsRunning(vm)
	if running && !sameEgressTarget(current, nextDecl) {
		return result, errors.New("stop the VM before setting or clearing egress")
	}
	if !running {
		stop, err := m.stopRuntime(ctx, vm)
		result.RuntimeChanged = stop.changed
		if err != nil {
			return result, err
		}
	}
	if nextDecl == nil {
		result.Changed, err = m.saveEgressDecl(vm, nil)
		return result, err
	}
	if nextDecl.Enabled {
		candidateVM := copyVMWithEgress(vm, nextDecl)
		validatedCA, err := m.validateEgress(ctx, candidateVM)
		if err != nil {
			return result, err
		}
		if running {
			return m.enableLiveEgress(ctx, vm, nextDecl, validatedCA, result)
		}
		result.Changed, err = m.saveReservedEgressDecl(vm, nextDecl)
		return result, err
	}
	if !running {
		if !sameEgressTarget(current, nextDecl) {
			candidateVM := copyVMWithEgress(vm, nextDecl)
			if _, err := m.validateEgress(ctx, candidateVM); err != nil {
				return result, err
			}
		}
		result.Changed, err = m.saveReservedEgressDecl(vm, nextDecl)
		return result, err
	}
	removed, removeErr := m.removeLiveEgress(ctx, vm)
	result.RuntimeChanged = result.RuntimeChanged || removed
	result.Changed, err = m.saveEgressDecl(vm, nextDecl)
	if err != nil {
		err = fmt.Errorf("disabled state was not saved; a restart may restore proxy access: %w", err)
	}
	return result, errors.Join(removeErr, err)
}

func newEgressResult(vm *state.VMRecord) EgressResult {
	return EgressResult{ID: vm.ID, Name: vm.Name}
}

func checkExpectedVMID(vm *state.VMRecord, expected string) error {
	if expected != "" && expected != vm.ID {
		return fmt.Errorf("VM %q has ID %s, expected %s", vm.Name, vm.ID, expected)
	}
	return nil
}

func copyEgressDecl(decl *egress.Decl) *egress.Decl {
	if decl == nil {
		return nil
	}
	cloned := *decl
	return &cloned
}

func sameEgressTarget(a, b *egress.Decl) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	left, right := *a, *b
	left.Enabled = false
	right.Enabled = false
	return left == right
}

func copyVMWithEgress(vm *state.VMRecord, nextDecl *egress.Decl) *state.VMRecord {
	candidateVM := *vm
	candidateVM.Network.Egress = copyEgressDecl(nextDecl)
	return &candidateVM
}

func (m *Manager) saveEgressDecl(vm *state.VMRecord, nextDecl *egress.Decl) (bool, error) {
	return m.saveEgressDeclChecked(vm, nextDecl, false)
}

func (m *Manager) saveReservedEgressDecl(vm *state.VMRecord, nextDecl *egress.Decl) (bool, error) {
	return m.saveEgressDeclChecked(vm, nextDecl, true)
}

func (m *Manager) saveEgressDeclChecked(vm *state.VMRecord, nextDecl *egress.Decl, checkReservation bool) (bool, error) {
	current := vm.Network.Egress
	if (current == nil && nextDecl == nil) || (current != nil && nextDecl != nil && *current == *nextDecl) {
		return false, nil
	}
	candidateVM := copyVMWithEgress(vm, nextDecl)
	candidateVM.UpdatedAt = time.Now().UTC()
	err := m.store.WithGlobalVM(vm.Name, vm.ID, func() error {
		if checkReservation {
			if err := m.CheckEgressAssignment(candidateVM, *nextDecl); err != nil {
				return err
			}
		}
		return m.store.SaveVM(candidateVM)
	})
	if err != nil {
		return false, err
	}
	*vm = *candidateVM
	return true, nil
}

func (m *Manager) finishEgressResult(action string, vm *state.VMRecord, result *EgressResult, err error) {
	result.Egress = copyEgressDecl(vm.Network.Egress)
	if !result.Changed && !result.RuntimeChanged {
		return
	}
	outcome := "succeeded"
	if err != nil {
		outcome = "failed"
	}
	enabled := result.Egress.IsEnabled()
	m.events.Emit(events.Event{Type: "egress", Action: action, Actor: events.Actor{ID: vm.ID, Attributes: map[string]string{"name": vm.Name, "changed": strconv.FormatBool(result.Changed), "runtimeChanged": strconv.FormatBool(result.RuntimeChanged), "outcome": outcome, "enabled": strconv.FormatBool(enabled)}}})
}

func (m *Manager) enableLiveEgress(ctx context.Context, vm *state.VMRecord, nextDecl *egress.Decl, validatedCA []byte, result EgressResult) (EgressResult, error) {
	rt := m.store.Runtime(vm)
	routes, err := gvproxy.GatewayRoutes(ctx, rt.NetworkSock())
	if err != nil {
		return result, err
	}
	exists := false
	for _, route := range routes {
		if route.Local == egress.Listener {
			if route.Target != nextDecl.BackendSocket {
				return result, errors.New("private gateway listener has a conflicting target")
			}
			exists = true
		}
	}
	if !exists {
		err = gvproxy.ExposeGateway(ctx, rt.NetworkSock(), gvproxy.GatewayRoute{Local: egress.Listener, Target: nextDecl.BackendSocket})
		if err != nil {
			if gvproxy.GatewayRejected(err) {
				return result, err
			}
			result.RuntimeChanged = true
			// An uncertain expose can finish after unexpose; stopping gvproxy fences the request.
			recovery, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			stop, stopErr := m.stopRuntime(recovery, vm)
			result.RuntimeChanged = result.RuntimeChanged || stop.changed
			return result, errors.Join(err, stopErr)
		}
		result.RuntimeChanged = true
	}
	changed, err := publishEgress(rt, validatedCA)
	result.RuntimeChanged = result.RuntimeChanged || changed
	if err != nil {
		return m.rollbackLiveEgress(vm, result, err)
	}
	candidateVM := copyVMWithEgress(vm, nextDecl)
	declarationChanged := *vm.Network.Egress != *nextDecl
	if declarationChanged || result.RuntimeChanged {
		if _, err := m.observeEgress(ctx, candidateVM, validatedCA); err != nil {
			return m.rollbackLiveEgress(vm, result, err)
		}
	}
	changed, err = m.saveReservedEgressDecl(vm, nextDecl)
	if err != nil {
		return m.rollbackLiveEgress(vm, result, err)
	}
	result.Changed = changed
	return result, nil
}

func (m *Manager) rollbackLiveEgress(vm *state.VMRecord, result EgressResult, cause error) (EgressResult, error) {
	recovery, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	removed, rollbackErr := m.removeLiveEgress(recovery, vm)
	result.RuntimeChanged = result.RuntimeChanged || removed
	return result, errors.Join(cause, rollbackErr)
}

func (m *Manager) removeLiveEgress(ctx context.Context, vm *state.VMRecord) (bool, error) {
	rt := m.store.Runtime(vm)
	routes, observeErr := gvproxy.GatewayRoutes(ctx, rt.NetworkSock())
	changed := observeErr != nil
	for _, r := range routes {
		if r.Local == egress.Listener {
			changed = true
		}
	}
	err := gvproxy.UnexposeGateway(ctx, rt.NetworkSock(), egress.Listener)
	if err != nil {
		recovery, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		stop, stopErr := m.stopRuntime(recovery, vm)
		changed = true
		if stopErr != nil {
			if !stop.gatewayTerminationConfirmed {
				err = fmt.Errorf("proxy disable not confirmed; inspect %s and %s: %w", rt.GVProxyProcessRecord(), m.store.LogPath(vm, "gvproxy"), err)
			}
			return changed, errors.Join(err, stopErr)
		}
	}
	filesChanged, filesErr := removeEgressFiles(rt)
	return changed || filesChanged, filesErr
}
