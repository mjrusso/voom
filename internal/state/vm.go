package state

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mjrusso/voom/internal/forward"
)

// HostPortAvailableFunc probes whether a host bind/port is available.
type HostPortAvailableFunc func(bind string, port int) (bool, string)

var defaultHostPortAvailable HostPortAvailableFunc = forward.HostPortAvailable

func (s *Store) portAvailable(bind string, port int) (bool, string) {
	if s.hostPortAvailable == nil {
		return forward.HostPortAvailable(bind, port)
	}
	return s.hostPortAvailable(bind, port)
}

// RegisterVM adds a name->ID mapping to the index and persists it.
func (s *Store) RegisterVM(name, id string) error {
	return s.WithGlobal(func() error { return s.RegisterVMLocked(name, id) })
}

// RegisterVMLocked adds a name->ID mapping while the caller holds the global lock.
func (s *Store) RegisterVMLocked(name, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.index.VMs[name]; exists {
		return fmt.Errorf("VM %q already exists", name)
	}
	s.index.VMs[name] = id
	if err := s.saveIndexLocked(); err != nil {
		delete(s.index.VMs, name)
		return err
	}
	return nil
}

// SaveVM validates and atomically writes the VM record to its on-disk vm.json.
func (s *Store) SaveVM(vm *VMRecord) error {
	if err := vm.validate(); err != nil {
		return err
	}
	return WriteJSONAtomic(filepath.Join(s.VMDir(vm.ID), "vm.json"), vm)
}

// LoadVM resolves a VM name through the index and loads its record from disk.
func (s *Store) LoadVM(name string) (*VMRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadVMLocked(name)
}

func (s *Store) loadVMLocked(name string) (*VMRecord, error) {
	id, ok := s.index.VMs[name]
	if !ok {
		return nil, fmt.Errorf("no such VM %q; run 'voom create %s --image <image>' first", name, name)
	}
	var vm VMRecord
	if err := ReadJSON(filepath.Join(s.VMDir(id), "vm.json"), &vm); err != nil {
		return nil, err
	}
	if vm.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("unsupported VM schema version %d", vm.SchemaVersion)
	}
	if vm.Name != name || vm.ID != id {
		return nil, fmt.Errorf("VM index mismatch for %q", name)
	}
	if err := vm.validate(); err != nil {
		return nil, err
	}
	return &vm, nil
}

// LoadVMByID loads a VM record directly by its ID, bypassing the name index.
func (s *Store) LoadVMByID(id string) (*VMRecord, error) {
	var vm VMRecord
	if err := ReadJSON(filepath.Join(s.VMDir(id), "vm.json"), &vm); err != nil {
		return nil, err
	}
	if vm.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("unsupported VM schema version %d", vm.SchemaVersion)
	}
	if err := vm.validate(); err != nil {
		return nil, err
	}
	return &vm, nil
}

// ListVMs returns all VM records in name order.
func (s *Store) ListVMs() ([]*VMRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listVMsLocked()
}

func (s *Store) listVMsLocked() ([]*VMRecord, error) {
	names := keys(s.index.VMs)
	out := []*VMRecord{}
	for _, n := range names {
		vm, err := s.loadVMLocked(n)
		if err != nil {
			return nil, err
		}
		out = append(out, vm)
	}
	return out, nil
}

// RenameVM renames a VM under its per-VM lock.
func (s *Store) RenameVM(ctx context.Context, oldName, newName string) (*VMRecord, error) {
	vm, unlock, err := s.LockVMRecord(ctx, oldName)
	if err != nil {
		return nil, err
	}
	defer unlock()
	previous := *vm
	next := *vm
	next.Name = newName
	next.UpdatedAt = time.Now().UTC()
	err = s.WithGlobalVM(oldName, vm.ID, func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.index.VMs[newName]; ok {
			return fmt.Errorf("VM %q already exists", newName)
		}
		if err := s.SaveVM(&next); err != nil {
			return err
		}
		delete(s.index.VMs, oldName)
		s.index.VMs[newName] = vm.ID
		if err := s.saveIndexLocked(); err != nil {
			delete(s.index.VMs, newName)
			s.index.VMs[oldName] = vm.ID
			return errors.Join(err, s.SaveVM(&previous))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	*vm = next
	return vm, nil
}

// DeleteVM unregisters a locked VM, then removes its files.
func (s *Store) DeleteVM(vm *VMRecord) error {
	if err := s.WithGlobalVM(vm.Name, vm.ID, func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.index.VMs, vm.Name)
		if err := s.saveIndexLocked(); err != nil {
			s.index.VMs[vm.Name] = vm.ID
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	_ = os.RemoveAll(s.RuntimeVMDir(vm))
	_ = os.RemoveAll(s.CacheVMDir(vm))
	return os.RemoveAll(s.VMDir(vm.ID))
}

// AllocateSSHPort returns the lowest free port in [SSHLow, SSHHigh] not reserved by another VM or in use on the host.
func (s *Store) AllocateSSHPort() (int, error) {
	for p := SSHLow; p <= SSHHigh; p++ {
		reserved, err := s.PortReserved("127.0.0.1", p)
		if err != nil {
			return 0, err
		}
		if reserved {
			continue
		}
		if ok, _ := s.portAvailable("127.0.0.1", p); !ok {
			continue
		}
		return p, nil
	}
	return 0, fmt.Errorf("no free SSH port in %d-%d", SSHLow, SSHHigh)
}

// EnsureSSHPortAvailable returns an error if port is invalid, reserved by another VM, or already in use on the host.
func (s *Store) EnsureSSHPortAvailable(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid SSH port %d", port)
	}
	reserved, err := s.PortReserved("127.0.0.1", port)
	if err != nil {
		return err
	}
	if reserved {
		return fmt.Errorf("SSH port %d is already used by another voom VM", port)
	}
	if ok, reason := s.portAvailable("127.0.0.1", port); !ok {
		return fmt.Errorf("SSH port %d: %s", port, reason)
	}
	return nil
}

// PortReserved reports whether any VM has reserved port on a bind that conflicts with the given bind.
func (s *Store) PortReserved(bind string, port int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vms, err := s.listVMsLocked()
	if err != nil {
		return false, err
	}
	for _, vm := range vms {
		if forward.BindsConflict(bind, vm.Network.SSHBind) && vm.Network.SSHPort == port {
			return true, nil
		}
		for _, f := range vm.Network.Forwards {
			if f.HostPort == port && forward.BindsConflict(bind, f.Bind) {
				return true, nil
			}
		}
	}
	return false, nil
}
