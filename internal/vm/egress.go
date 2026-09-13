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
	"github.com/mjrusso/voom/internal/host"
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

// EgressOptions guards the target identity and controls the state of a new declaration.
type EgressOptions struct {
	ExpectedID string
	Disabled   bool
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
	im, err := m.store.LoadImageByID(vm.Image.ID)
	if err != nil {
		return nil, err
	}
	if !im.Capabilities.ControlShare {
		return nil, errors.New("egress requires an image with controlShare capability")
	}
	if err := egress.ProbeSocket(ctx, d.BackendSocket); err != nil {
		return nil, err
	}
	return egress.ReadCA(d.CACertPath)
}

func (m *Manager) validateEgress(ctx context.Context, vm *state.VMRecord, running bool) ([]byte, error) {
	ca, err := m.validateEgressTarget(ctx, vm)
	if err != nil {
		return nil, err
	}
	if running {
		if _, ok := process.ValidRecord(m.store.Runtime(vm).GVProxyProcessRecord(), "gvproxy"); !ok {
			return nil, errors.New("VM gvproxy process identity is unavailable")
		}
		err = gvproxy.CheckProcess(ctx, m.store.Runtime(vm).NetworkSock(), gvproxy.GuestIsolationCapability, gvproxy.GatewayCapability)
	} else {
		executable, resolveErr := host.ExePath("gvproxy")
		err = resolveErr
		if err == nil {
			err = gvproxy.CheckExecutable(ctx, executable, gvproxy.GuestIsolationCapability, gvproxy.GatewayCapability)
		}
	}
	if err != nil {
		return nil, err
	}
	return ca, nil
}

// SetEgress attaches a unique backend to a stopped VM. Callers must not hold state locks.
func (m *Manager) SetEgress(ctx context.Context, name, socket, ca string, opts EgressOptions) (result EgressResult, retErr error) {
	lock, err := m.lockVMState(ctx, name)
	if err != nil {
		return result, err
	}
	defer lock.Release()
	vm := lock.VM
	result = newEgressResult(vm)
	defer func() { m.finishEgressResult("set", vm, &result, retErr) }()
	if err := checkExpectedVMID(vm, opts.ExpectedID); err != nil {
		return result, err
	}
	if m.IsRunning(vm) {
		return result, errors.New("stop the VM before setting or clearing egress")
	}
	stop, err := m.stopRuntime(ctx, vm)
	result.RuntimeChanged = stop.changed
	if err != nil {
		return result, err
	}
	nextDecl, err := egress.Normalize(socket, ca)
	if err != nil {
		return result, err
	}
	nextDecl.Enabled = !opts.Disabled
	candidateVM := copyVMWithEgress(vm, &nextDecl)
	if err := m.CheckEgressAssignment(candidateVM, nextDecl); err != nil {
		return result, err
	}
	if _, err := m.validateEgress(ctx, candidateVM, false); err != nil {
		return result, err
	}
	result.Changed, err = m.saveEgressDecl(vm, &nextDecl)
	return result, err
}

// ClearEgress releases a stopped VM's attachment after runtime cleanup.
func (m *Manager) ClearEgress(ctx context.Context, name string, opts EgressOptions) (result EgressResult, retErr error) {
	lock, err := m.lockVMState(ctx, name)
	if err != nil {
		return result, err
	}
	defer lock.Release()
	vm := lock.VM
	result = newEgressResult(vm)
	defer func() { m.finishEgressResult("clear", vm, &result, retErr) }()
	if err := checkExpectedVMID(vm, opts.ExpectedID); err != nil {
		return result, err
	}
	if m.IsRunning(vm) {
		return result, errors.New("stop the VM before setting or clearing egress")
	}
	stop, err := m.stopRuntime(ctx, vm)
	result.RuntimeChanged = stop.changed
	if err != nil {
		return result, err
	}
	result.Changed, err = m.saveEgressDecl(vm, nil)
	return result, err
}

// EnableEgress validates and reconciles the attachment without restarting healthy runtime.
func (m *Manager) EnableEgress(ctx context.Context, name string, opts EgressOptions) (result EgressResult, retErr error) {
	lock, err := m.lockVMState(ctx, name)
	if err != nil {
		return result, err
	}
	defer lock.Release()
	vm := lock.VM
	result = newEgressResult(vm)
	defer func() { m.finishEgressResult("enable", vm, &result, retErr) }()
	if err := checkExpectedVMID(vm, opts.ExpectedID); err != nil {
		return result, err
	}
	if vm.Network.Egress == nil {
		return result, errors.New("egress is not configured")
	}
	nextDecl := *vm.Network.Egress
	nextDecl.Enabled = true
	candidateVM := copyVMWithEgress(vm, &nextDecl)
	running := m.IsRunning(vm)
	if !running {
		stop, stopErr := m.stopRuntime(ctx, vm)
		result.RuntimeChanged = stop.changed
		if stopErr != nil {
			return result, stopErr
		}
	}
	if err := m.CheckEgressAssignment(candidateVM, nextDecl); err != nil {
		return result, err
	}
	if running {
		lock.ReleaseGlobal()
	}
	validatedCA, err := m.validateEgress(ctx, candidateVM, running)
	if err != nil {
		return result, err
	}
	if !running {
		result.Changed, err = m.saveEgressDecl(vm, &nextDecl)
		return result, err
	}
	return m.enableLiveEgress(ctx, vm, &nextDecl, validatedCA, result)
}

// DisableEgress revokes attachment access; direct networking remains available.
func (m *Manager) DisableEgress(ctx context.Context, name string, opts EgressOptions) (result EgressResult, retErr error) {
	lock, err := m.lockVM(ctx, name)
	if err != nil {
		return result, err
	}
	defer lock.Release()
	vm := lock.VM
	result = newEgressResult(vm)
	defer func() { m.finishEgressResult("disable", vm, &result, retErr) }()
	if err := checkExpectedVMID(vm, opts.ExpectedID); err != nil {
		return result, err
	}
	if vm.Network.Egress == nil {
		return result, errors.New("egress is not configured")
	}
	nextDecl := *vm.Network.Egress
	nextDecl.Enabled = false
	if !m.IsRunning(vm) {
		stop, stopErr := m.stopRuntime(ctx, vm)
		result.RuntimeChanged = stop.changed
		if stopErr != nil {
			return result, stopErr
		}
		result.Changed, err = m.saveEgressDecl(vm, &nextDecl)
		return result, err
	}
	removal, removeErr := m.removeLiveEgress(ctx, vm)
	result.RuntimeChanged = result.RuntimeChanged || removal.changed
	result.Changed, err = m.saveEgressDecl(vm, &nextDecl)
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

func copyVMWithEgress(vm *state.VMRecord, nextDecl *egress.Decl) *state.VMRecord {
	candidateVM := *vm
	candidateVM.Network.Egress = copyEgressDecl(nextDecl)
	return &candidateVM
}

func (m *Manager) saveEgressDecl(vm *state.VMRecord, nextDecl *egress.Decl) (bool, error) {
	current := vm.Network.Egress
	if (current == nil && nextDecl == nil) || (current != nil && nextDecl != nil && *current == *nextDecl) {
		return false, nil
	}
	candidateVM := copyVMWithEgress(vm, nextDecl)
	candidateVM.UpdatedAt = time.Now().UTC()
	if err := m.store.SaveVM(candidateVM); err != nil {
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
	enabled := result.Egress != nil && result.Egress.Enabled
	m.events.Emit(events.Event{Type: "egress", Action: action, Actor: events.Actor{ID: vm.ID, Attributes: map[string]string{"name": vm.Name, "changed": strconv.FormatBool(result.Changed), "runtimeChanged": strconv.FormatBool(result.RuntimeChanged), "outcome": outcome, "enabled": strconv.FormatBool(enabled)}}})
}

func (m *Manager) enableLiveEgress(ctx context.Context, vm *state.VMRecord, nextDecl *egress.Decl, validatedCA []byte, result EgressResult) (EgressResult, error) {
	original := copyEgressDecl(vm.Network.Egress)
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
	changed, err := m.saveEgressDecl(vm, nextDecl)
	if err != nil {
		return result, err
	}
	result.Changed = result.Changed || changed
	uncertainExpose, rejectedExpose := false, false
	if !exists {
		err = gvproxy.ExposeGateway(ctx, rt.NetworkSock(), gvproxy.GatewayRoute{Local: egress.Listener, Target: nextDecl.BackendSocket})
		rejectedExpose = gvproxy.GatewayRejected(err)
		uncertainExpose = err != nil && !rejectedExpose
		result.RuntimeChanged = result.RuntimeChanged || !rejectedExpose
	}
	if err == nil {
		var changed bool
		changed, err = publishEgress(rt, true, validatedCA)
		result.RuntimeChanged = result.RuntimeChanged || changed
	}
	if err == nil && (result.Changed || result.RuntimeChanged) {
		_, err = m.observeEgress(ctx, vm, validatedCA)
	}
	if err == nil {
		return result, nil
	}
	recovery, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var rollback error
	removalConfirmed := true
	if uncertainExpose {
		// A timed-out expose can execute after unexpose; only process exit fences that request.
		stop, stopErr := m.stopRuntime(recovery, vm)
		result.RuntimeChanged = result.RuntimeChanged || stop.changed
		removalConfirmed = stop.gatewayTerminationConfirmed
		rollback = stopErr
	} else if !rejectedExpose {
		var removal egressRemovalResult
		removal, rollback = m.removeLiveEgress(recovery, vm)
		result.RuntimeChanged = result.RuntimeChanged || removal.changed
		removalConfirmed = removal.confirmed
	}
	filesChanged, filesErr := removeEgressFiles(rt)
	result.RuntimeChanged = result.RuntimeChanged || filesChanged
	rollback = errors.Join(rollback, filesErr)
	if !removalConfirmed && original != nil {
		original.Enabled = false
	}
	if result.Changed || rollback != nil {
		_, saveErr := m.saveEgressDecl(vm, original)
		rollback = errors.Join(rollback, saveErr)
	}
	if vm.Network.Egress != nil && vm.Network.Egress.Enabled && rollback == nil {
		err = fmt.Errorf("%w; desired egress remains enabled but runtime is not synchronized; run voom config egress enable %s after recovery", err, vm.Name)
	}
	return result, errors.Join(err, rollback)
}

type egressRemovalResult struct {
	changed   bool
	confirmed bool
}

func (m *Manager) removeLiveEgress(ctx context.Context, vm *state.VMRecord) (egressRemovalResult, error) {
	rt := m.store.Runtime(vm)
	routes, observeErr := gvproxy.GatewayRoutes(ctx, rt.NetworkSock())
	result := egressRemovalResult{changed: observeErr != nil}
	for _, r := range routes {
		if r.Local == egress.Listener {
			result.changed = true
		}
	}
	err := gvproxy.UnexposeGateway(ctx, rt.NetworkSock(), egress.Listener)
	if err != nil {
		recovery, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		stop, stopErr := m.stopRuntime(recovery, vm)
		result.changed = true
		result.confirmed = stop.gatewayTerminationConfirmed
		if stopErr != nil {
			if !result.confirmed {
				err = fmt.Errorf("proxy disable not confirmed; inspect %s and %s: %w", rt.GVProxyProcessRecord(), m.store.LogPath(vm, "gvproxy"), err)
			}
			return result, errors.Join(err, stopErr)
		}
	} else {
		result.confirmed = true
	}
	filesChanged, filesErr := removeEgressFiles(rt)
	result.changed = result.changed || filesChanged
	return result, filesErr
}
