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
	Name           string
	Changed        bool
	RuntimeChanged bool
	Enabled        bool
	Egress         *egress.Decl
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

func (m *Manager) validateEgress(ctx context.Context, vm *state.VMRecord, executable string, running bool) ([]byte, error) {
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
	if running {
		if _, ok := process.ValidRecord(m.store.Runtime(vm).GVProxyProcessRecord(), "gvproxy"); !ok {
			return nil, errors.New("VM gvproxy process identity is unavailable")
		}
		err = gvproxy.CheckProcess(ctx, m.store.Runtime(vm).NetworkSock(), gvproxy.GuestIsolationCapability, gvproxy.GatewayCapability)
	} else {
		if executable == "" {
			executable, err = host.ExePath("gvproxy")
		}
		if err == nil {
			err = gvproxy.CheckExecutable(ctx, executable, gvproxy.GuestIsolationCapability, gvproxy.GatewayCapability)
		}
	}
	if err != nil {
		return nil, err
	}
	if err := egress.ProbeSocket(ctx, d.BackendSocket); err != nil {
		return nil, err
	}
	return egress.ReadCA(d.CACertPath)
}

type egressAction string

// SetEgress attaches a unique backend to a stopped VM. Callers must not hold state locks.
func (m *Manager) SetEgress(ctx context.Context, name, socket, ca string) (EgressResult, error) {
	return m.configureEgress(ctx, name, "set", socket, ca)
}

// ClearEgress releases a stopped VM's attachment after runtime cleanup.
func (m *Manager) ClearEgress(ctx context.Context, name string) (EgressResult, error) {
	return m.configureEgress(ctx, name, "clear", "", "")
}

// EnableEgress validates and reconciles the attachment without restarting healthy runtime.
func (m *Manager) EnableEgress(ctx context.Context, name string) (EgressResult, error) {
	return m.configureEgress(ctx, name, "enable", "", "")
}

// DisableEgress revokes attachment access; direct networking remains available.
func (m *Manager) DisableEgress(ctx context.Context, name string) (EgressResult, error) {
	return m.configureEgress(ctx, name, "disable", "", "")
}

func (m *Manager) configureEgress(ctx context.Context, name string, action egressAction, socket, ca string) (result EgressResult, retErr error) {
	lock, err := m.lockVMState(ctx, name)
	if err != nil {
		return result, err
	}
	defer lock.Release()
	vm := lock.VM
	result.Name = vm.Name
	original := vm.Network.Egress
	if original != nil {
		previous := *original
		original = &previous
	}
	defer func() {
		result.Egress = vm.Network.Egress
		if retErr != nil {
			if saved, err := m.store.LoadVMByID(vm.ID); err == nil {
				result.Egress = saved.Network.Egress
			}
		}
		result.Enabled = result.Egress != nil && result.Egress.Enabled
		if result.Changed || result.RuntimeChanged {
			outcome := "succeeded"
			if retErr != nil {
				outcome = "failed"
			}
			m.events.Emit(events.Event{Type: "egress", Action: string(action), Actor: events.Actor{ID: vm.ID, Attributes: map[string]string{"name": vm.Name, "changed": strconv.FormatBool(result.Changed), "runtimeChanged": strconv.FormatBool(result.RuntimeChanged), "outcome": outcome, "enabled": strconv.FormatBool(result.Enabled)}}})
		}
	}()
	running := m.IsRunning(vm)
	if (action == "set" || action == "clear") && running {
		return result, errors.New("stop the VM before setting or clearing egress")
	}
	if action != "set" && action != "clear" && original == nil {
		return result, errors.New("egress is not configured")
	}
	if !running {
		stop, err := m.stopRuntime(ctx, vm)
		result.RuntimeChanged = stop.changed
		if err != nil {
			return result, err
		}
	}
	switch action {
	case "set":
		decl, err := egress.Normalize(socket, ca)
		if err != nil {
			return result, err
		}
		vm.Network.Egress = &decl
	case "clear":
		vm.Network.Egress = nil
	case "enable":
		vm.Network.Egress.Enabled = true
	case "disable":
		vm.Network.Egress.Enabled = false
	default:
		return result, fmt.Errorf("unknown egress action %q", action)
	}
	var validatedCA []byte
	if action == "set" || action == "enable" {
		err = m.CheckEgressAssignment(vm, *vm.Network.Egress)
		if err == nil {
			validatedCA, err = m.validateEgress(ctx, vm, "", running)
		}
		if err != nil {
			vm.Network.Egress = original
			return result, err
		}
	}
	if running {
		lock.ReleaseGlobal()
	}
	persistentChanged := (original == nil) != (vm.Network.Egress == nil) || (original != nil && vm.Network.Egress != nil && *original != *vm.Network.Egress)
	save := func() error {
		if !persistentChanged {
			return nil
		}
		previous := vm.UpdatedAt
		vm.UpdatedAt = time.Now().UTC()
		if err := m.store.SaveVM(vm); err != nil {
			vm.UpdatedAt = previous
			return err
		}
		result.Changed = true
		return nil
	}
	if !running {
		return result, save()
	}
	rt := m.store.Runtime(vm)
	if action == "disable" {
		changed, removeErr := m.removeLiveEgress(ctx, vm)
		result.RuntimeChanged = result.RuntimeChanged || changed
		saveErr := save()
		if saveErr != nil {
			saveErr = fmt.Errorf("disabled state was not saved; a restart may restore proxy access: %w", saveErr)
		}
		return result, errors.Join(removeErr, saveErr)
	}
	routes, err := gvproxy.GatewayRoutes(ctx, rt.NetworkSock())
	if err != nil {
		vm.Network.Egress = original
		return result, err
	}
	exists := false
	for _, route := range routes {
		if route.Local == egress.Listener {
			if route.Target != vm.Network.Egress.BackendSocket {
				vm.Network.Egress = original
				return result, errors.New("private gateway listener has a conflicting target")
			}
			exists = true
		}
	}
	if err = save(); err != nil {
		vm.Network.Egress = original
		return result, err
	}
	uncertainExpose, rejectedExpose := false, false
	if !exists {
		err = gvproxy.ExposeGateway(ctx, rt.NetworkSock(), gvproxy.GatewayRoute{Local: egress.Listener, Target: vm.Network.Egress.BackendSocket})
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
	if uncertainExpose {
		// A timed-out expose can execute after unexpose; only process exit fences that request.
		stop, stopErr := m.stopRuntime(recovery, vm)
		result.RuntimeChanged = result.RuntimeChanged || stop.changed
		rollback = stopErr
		if !stop.gatewayTerminationConfirmed {
			rollback = unconfirmedEgressRemoval{rollback}
		}
	} else if !rejectedExpose {
		_, rollback = m.removeLiveEgress(recovery, vm)
	}
	filesChanged, filesErr := removeEgressFiles(rt)
	result.RuntimeChanged = result.RuntimeChanged || filesChanged
	rollback = errors.Join(rollback, filesErr)
	vm.Network.Egress = original
	var unconfirmed unconfirmedEgressRemoval
	if errors.As(rollback, &unconfirmed) && vm.Network.Egress != nil {
		vm.Network.Egress.Enabled = false
	}
	if result.Changed || rollback != nil {
		vm.UpdatedAt = time.Now().UTC()
		rollback = errors.Join(rollback, m.store.SaveVM(vm))
	}
	if vm.Network.Egress != nil && vm.Network.Egress.Enabled && rollback == nil {
		err = fmt.Errorf("%w; desired egress remains enabled but runtime is not synchronized; run voom config egress enable %s after recovery", err, vm.Name)
	}
	return result, errors.Join(err, rollback)
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
		if !stop.gatewayTerminationConfirmed {
			return true, unconfirmedEgressRemoval{errors.Join(fmt.Errorf("proxy disable not confirmed; inspect %s and %s", rt.GVProxyProcessRecord(), m.store.LogPath(vm, "gvproxy")), err, stopErr)}
		}
		changed = true
	}
	filesChanged, filesErr := removeEgressFiles(rt)
	return changed || filesChanged, filesErr
}

type unconfirmedEgressRemoval struct{ error }
