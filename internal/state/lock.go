package state

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

func (s *Store) lockGlobal() (func(), error) {
	return flock(filepath.Join(s.paths.State, "locks", "state.lock"))
}

// WithGlobal runs fn with the current index under the store-wide lock.
func (s *Store) WithGlobal(fn func() error) error {
	unlock, err := s.lockGlobal()
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.reloadIndex(); err != nil {
		return err
	}
	return fn()
}

// WithGlobalVM runs fn only if name still identifies the VM whose lock the caller holds.
func (s *Store) WithGlobalVM(name, id string, fn func() error) error {
	return s.WithGlobal(func() error {
		s.mu.Lock()
		current := s.index.VMs[name]
		s.mu.Unlock()
		if current != id {
			return fmt.Errorf("VM %q no longer identifies %s", name, id)
		}
		return fn()
	})
}

// LockVMRecord locks the VM currently resolved by name and reloads it by ID.
func (s *Store) LockVMRecord(ctx context.Context, name string) (*VMRecord, func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		index, err := s.readIndex()
		if err != nil {
			return nil, nil, err
		}
		id, ok := index.VMs[name]
		if !ok {
			return nil, nil, fmt.Errorf("no such VM %q; run 'voom create %s --image <image>' first", name, name)
		}
		unlock, err := s.TryLockVM(id)
		if err != nil {
			return nil, nil, err
		}
		if unlock != nil {
			unlock = sync.OnceFunc(unlock)
			index, err = s.readIndex()
			if err != nil {
				unlock()
				return nil, nil, err
			}
			if index.VMs[name] != id {
				unlock()
				return nil, nil, fmt.Errorf("VM %q changed while waiting for its lock", name)
			}
			vm, err := s.LoadVMByID(id)
			if err != nil {
				unlock()
				return nil, nil, err
			}
			if vm.Name != name {
				unlock()
				return nil, nil, fmt.Errorf("VM index mismatch for %q", name)
			}
			return vm, unlock, nil
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// TryLockVM returns a nil release function when another operation holds the VM lock.
func (s *Store) TryLockVM(id string) (func(), error) {
	unlock, err := flockMode(filepath.Join(s.paths.State, "locks", "vms", id+".lock"), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return nil, nil
	}
	return unlock, err
}

func flock(path string) (func(), error) { return flockMode(path, unix.LOCK_EX) }
func flockMode(path string, mode int) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), mode); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
