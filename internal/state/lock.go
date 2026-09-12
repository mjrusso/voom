package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// LockGlobal acquires an exclusive flock on the store-wide lock file and returns a release func.
func (s *Store) LockGlobal() (func(), error) {
	return flock(filepath.Join(s.paths.State, "locks", "state.lock"))
}

// LockGlobalAndReload acquires the store-wide lock and refreshes the in-memory
// index from disk before returning.
func (s *Store) LockGlobalAndReload() (func(), error) {
	unlock, err := s.LockGlobal()
	if err != nil {
		return nil, err
	}
	if err := s.reloadIndex(); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

// LockVM acquires an exclusive flock on the per-VM lock file and returns a release func.
func (s *Store) LockVM(id string) (func(), error) {
	return flock(filepath.Join(s.paths.State, "locks", "vms", id+".lock"))
}

// LockVMRecord locks the VM currently resolved by name and reloads it by ID.
func (s *Store) LockVMRecord(ctx context.Context, name string) (*VMRecord, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	vm, err := s.LoadVM(name)
	if err != nil {
		return nil, nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		unlock, err := s.TryLockVM(vm.ID)
		if err != nil {
			return nil, nil, err
		}
		if unlock != nil {
			unlock = sync.OnceFunc(unlock)
			vm, err = s.LoadVMByID(vm.ID)
			if err != nil {
				unlock()
				return nil, nil, err
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
