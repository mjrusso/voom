package state

import (
	"os"
	"path/filepath"
)

// ImageDir returns the on-disk directory holding the image with the given ID.
func (s *Store) ImageDir(id string) string {
	return filepath.Join(s.paths.State, "images", id)
}

// VMDir returns the on-disk directory holding the VM record with the given ID.
func (s *Store) VMDir(id string) string {
	return filepath.Join(s.paths.State, "vms", id)
}

// RuntimeVMDir returns the per-VM runtime directory used for process records and sockets.
func (s *Store) RuntimeVMDir(vm *VMRecord) string {
	return filepath.Join(s.paths.Runtime, "vms", vm.ID)
}

// ControlShareDir holds configuration and reports; only legacy images export it.
func (s *Store) ControlShareDir(vm *VMRecord) string {
	return filepath.Join(s.RuntimeVMDir(vm), "control")
}

// SeedImagePath returns the path to the cloud-init seed image for the VM.
func (s *Store) SeedImagePath(vm *VMRecord) string {
	return filepath.Join(s.RuntimeVMDir(vm), "seed.img")
}

// ControlSharePortsPath is the listener cache used by the forward reconciler.
func (s *Store) ControlSharePortsPath(vm *VMRecord) string {
	return filepath.Join(s.ControlShareDir(vm), "ports.json")
}

// ControlShareMountsPath is the staged manifest sent to the guest.
func (s *Store) ControlShareMountsPath(vm *VMRecord) string {
	return filepath.Join(s.ControlShareDir(vm), "mounts.json")
}

// CacheVMDir returns the per-VM cache directory used for logs and other recreatable files.
func (s *Store) CacheVMDir(vm *VMRecord) string {
	return filepath.Join(s.paths.Cache, "vms", vm.ID)
}

// ImageDiskPath returns the on-disk path to the image's disk file.
func (s *Store) ImageDiskPath(im *ImageRecord) string {
	return filepath.Join(s.ImageDir(im.ID), im.Disk)
}

// VMDiskPath returns the VM's persistent boot disk path.
func (s *Store) VMDiskPath(vm *VMRecord) string {
	path := filepath.Join(s.VMStorageDir(vm), "disk."+DiskExtension(vm.Driver))
	if vm.Driver == "vfkit" && vm.MacOS == nil && vm.StorageGeneration == "" {
		// Existing Linux VMs keep their disk.img path until reset.
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			legacy := filepath.Join(s.VMDir(vm.ID), "disk.img")
			if _, err := os.Lstat(legacy); err == nil {
				return legacy
			}
		}
	}
	return path
}

// LogPath returns the path for the given log kind (serial, qemu, vfkit, gvproxy, share-mount)
// under the VM's cache directory.
func (s *Store) LogPath(vm *VMRecord, kind string) string {
	names := map[string]string{"serial": "serial.log", "qemu": "qemu.log", "vfkit": "vfkit.log", "gvproxy": "gvproxy.log", "share-mount": "share-mount.log"}
	n, ok := names[kind]
	if !ok {
		n = kind + ".log"
	}
	return filepath.Join(s.CacheVMDir(vm), n)
}
