package state

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// WithGlobal runs fn with the current index under the store-wide lock.
func (s *Store) WithGlobal(fn func() error) error {
	return s.WithGlobalContext(context.Background(), fn)
}

// WithGlobalContext acquires the store-wide lock with ctx and runs fn with the current index.
func (s *Store) WithGlobalContext(ctx context.Context, fn func() error) error {
	return s.withGlobalLock(ctx, func() error {
		if err := s.reloadIndex(); err != nil {
			return err
		}
		return fn()
	})
}

func (s *Store) withGlobalLock(ctx context.Context, fn func() error) error {
	unlock, err := flockContext(ctx, filepath.Join(s.paths.State, "locks", "state.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

// WithGlobalVM runs fn only if name still identifies the VM whose lock the caller holds.
func (s *Store) WithGlobalVM(name, id string, fn func() error) error {
	return s.WithGlobalVMContext(context.Background(), name, id, fn)
}

// WithGlobalVMContext acquires the global lock with ctx and verifies the VM's identity before running fn.
func (s *Store) WithGlobalVMContext(ctx context.Context, name, id string, fn func() error) error {
	return s.WithGlobalContext(ctx, func() error {
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
		index, err := ReadIndex(s.paths.State)
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
			index, err = ReadIndex(s.paths.State)
			if err != nil {
				unlock()
				return nil, nil, err
			}
			if index.VMs[name] != id {
				unlock()
				return nil, nil, fmt.Errorf("VM %q changed while waiting for its lock", name)
			}
			vm, err := s.readVM(id, name)
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

// LockResources locks opaque resource keys in stable order until the returned function is called.
func (s *Store) LockResources(ctx context.Context, resources []string) (func(), error) {
	unique := make(map[string]struct{}, len(resources))
	for _, resource := range resources {
		unique[resource] = struct{}{}
	}
	ordered := make([]string, 0, len(unique))
	for resource := range unique {
		ordered = append(ordered, resource)
	}
	sort.Strings(ordered)

	unlocks := make([]func(), 0, len(ordered))
	for _, resource := range ordered {
		digest := sha256.Sum256([]byte(resource))
		unlock, err := flockContext(ctx, filepath.Join(s.paths.State, "locks", "resources", fmt.Sprintf("%x.lock", digest)))
		if err != nil {
			for i := len(unlocks) - 1; i >= 0; i-- {
				unlocks[i]()
			}
			return nil, err
		}
		unlocks = append(unlocks, unlock)
	}
	return sync.OnceFunc(func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}), nil
}

func flockContext(ctx context.Context, path string) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		unlock, err := flockMode(path, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return unlock, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return nil, err
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

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
