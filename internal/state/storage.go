package state

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func validStorageGeneration(id string) bool {
	_, err := hex.DecodeString(id)
	return len(id) == 32 && err == nil
}

// VMStorageDir resolves the active guest boot storage.
func (s *Store) VMStorageDir(vm *VMRecord) string {
	if vm.StorageGeneration != "" {
		return filepath.Join(s.VMDir(vm.ID), "storage-"+vm.StorageGeneration)
	}
	return s.VMDir(vm.ID)
}

// CopyStorageGeneration stages durable replacement storage without changing the active VM record.
func (s *Store) CopyStorageGeneration(ctx context.Context, vm *VMRecord, platform *MacOSPlatform, sourceDisk string) (_ *VMRecord, retErr error) {
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	next := *vm
	next.SchemaVersion = storageRecordVersion
	next.MacOS = platform
	next.StorageGeneration = id
	dir := s.VMStorageDir(&next)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil, err
	}
	defer func() {
		if retErr != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	if platform != nil {
		next.MacOS, err = platform.CopyVM(ctx, filepath.Dir(sourceDisk), dir)
	} else {
		err = CopyFile(ctx, sourceDisk, s.VMDiskPath(&next))
	}
	if err != nil {
		return nil, err
	}
	if err := syncDir(s.VMDir(vm.ID)); err != nil {
		return nil, err
	}
	return &next, nil
}

// ReplaceStorage commits prepared VM metadata with replacement boot storage.
// The caller must hold the VM lock and confirm runtime termination.
func (s *Store) ReplaceStorage(ctx context.Context, vm, candidate *VMRecord, sourceDisk string) error {
	if vm.MacOS != nil {
		if err := WaitMacOSStorage(ctx, s.VMStorageDir(vm)); err != nil {
			return err
		}
	}
	next, err := s.CopyStorageGeneration(ctx, candidate, candidate.MacOS, sourceDisk)
	if err != nil {
		return err
	}
	preserve := false
	defer func() {
		if !preserve {
			_ = os.RemoveAll(s.VMStorageDir(next))
		}
	}()
	if err := s.WithGlobalVM(vm.Name, vm.ID, func() error {
		if s.IndexSnapshot().Images[next.Image.Name] != next.Image.ID {
			return fmt.Errorf("image %q changed while resetting VM", next.Image.Name)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if next.MacOS != nil {
			if err := s.CheckMacOSIdentityAvailable(next.MacOS); err != nil {
				return err
			}
		}
		// SaveVM can fail after rename; retain both generations if commit durability is uncertain.
		preserve = true
		return s.SaveVM(next)
	}); err != nil {
		return err
	}
	*vm = *next
	if err := s.CleanupStorage(vm); err != nil {
		return fmt.Errorf("reset committed; cleaning up previous storage: %w", err)
	}
	return nil
}

// CleanupStorage requires the VM lock and confirmed runtime termination.
// An interrupted reset leaves unreferenced generations for the next start or reset.
func (s *Store) CleanupStorage(vm *VMRecord) error {
	if vm.MacOS != nil {
		if err := vm.MacOS.ValidateStorage(s.VMStorageDir(vm)); err != nil {
			return err
		}
	} else {
		info, err := os.Lstat(s.VMDiskPath(vm))
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("VM boot disk is not a regular file")
		}
	}
	// A previous record write may have renamed successfully before its directory sync failed.
	if err := syncPath(filepath.Join(s.VMDir(vm.ID), "vm.json")); err != nil {
		return err
	}
	if err := syncDir(s.VMDir(vm.ID)); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.VMDir(vm.ID))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		id, ok := strings.CutPrefix(entry.Name(), "storage-")
		if ok && validStorageGeneration(id) && id != vm.StorageGeneration {
			if err := os.RemoveAll(filepath.Join(s.VMDir(vm.ID), entry.Name())); err != nil {
				return err
			}
		}
	}
	if vm.StorageGeneration != "" {
		for _, name := range []string{"disk.raw", "disk.img", "disk.qcow2"} {
			if err := os.Remove(filepath.Join(s.VMDir(vm.ID), name)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return syncDir(s.VMDir(vm.ID))
}
