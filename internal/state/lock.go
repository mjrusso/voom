package state

import (
	"os"
	"path/filepath"

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

func flock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
