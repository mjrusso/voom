package state

import (
	"path/filepath"

	"github.com/mjrusso/voom/internal/forward"
)

// RuntimeLayout resolves per-VM runtime file paths (pids, sockets, monitors) under one base dir.
type RuntimeLayout struct {
	dir string
}

// Runtime returns the RuntimeLayout rooted at the VM's runtime directory.
func (s *Store) Runtime(vm *VMRecord) RuntimeLayout {
	return RuntimeLayout{dir: s.RuntimeVMDir(vm)}
}

// Dir returns the runtime base directory.
func (r RuntimeLayout) Dir() string {
	return r.dir
}

// VMPid returns the path to the VM process pidfile.
func (r RuntimeLayout) VMPid() string {
	return filepath.Join(r.dir, "vm.pid")
}

// GVProxyPid returns the path to the gvproxy pidfile.
func (r RuntimeLayout) GVProxyPid() string {
	return filepath.Join(r.dir, "gvproxy.pid")
}

// NetworkSock returns the path to the gvproxy network socket.
func (r RuntimeLayout) NetworkSock() string {
	return filepath.Join(r.dir, "network.sock")
}

// DriverNetSock returns the driver-specific network socket path (QEMU or vfkit).
func (r RuntimeLayout) DriverNetSock(driver string) string {
	if driver == "vfkit" {
		return r.VFKitNetSock()
	}
	return r.QEMUNetSock()
}

// QEMUNetSock returns the path to the QEMU network socket.
func (r RuntimeLayout) QEMUNetSock() string {
	return filepath.Join(r.dir, "qemu-net.sock")
}

// VFKitNetSock returns the path to the vfkit network socket.
func (r RuntimeLayout) VFKitNetSock() string {
	return filepath.Join(r.dir, "vfkit-net.sock")
}

// QEMUMonitor returns the path to the QEMU monitor socket.
func (r RuntimeLayout) QEMUMonitor() string {
	return filepath.Join(r.dir, "qemu.mon")
}

// VFKitRestSock returns the path to the vfkit REST control socket.
func (r RuntimeLayout) VFKitRestSock() string {
	return filepath.Join(r.dir, "vfkit.sock")
}

// EFIStore returns the path to the EFI variable store file.
func (r RuntimeLayout) EFIStore() string {
	return filepath.Join(r.dir, "efi-variable-store")
}

// AutoForwardPid returns the pidfile path used by the auto-forward watcher.
func (r RuntimeLayout) AutoForwardPid() string {
	return forward.WatcherPidfile(r.dir)
}

// AutoForwardsJSON returns the path to the auto-forward runtime state JSON.
func (r RuntimeLayout) AutoForwardsJSON() string {
	return forward.RuntimeStatePath(r.dir)
}

// VirtiofsPidGlob returns a glob pattern matching virtiofs daemon pidfiles.
func (r RuntimeLayout) VirtiofsPidGlob() string {
	return filepath.Join(r.dir, "virtiofs-*.pid")
}

// VirtiofsSockGlob returns a glob pattern matching virtiofs daemon sockets.
func (r RuntimeLayout) VirtiofsSockGlob() string {
	return filepath.Join(r.dir, "virtiofs-*.sock")
}

// DriverSockets returns the union of driver and network socket paths used by both drivers.
func (r RuntimeLayout) DriverSockets() []string {
	return []string{r.NetworkSock(), r.QEMUNetSock(), r.QEMUMonitor(), r.VFKitNetSock(), r.VFKitRestSock()}
}

// VMArtifacts returns the pidfile and driver-specific control endpoint paths for a running VM.
func (r RuntimeLayout) VMArtifacts(driver string) []string {
	out := []string{r.VMPid()}
	switch driver {
	case "qemu":
		out = append(out, r.QEMUMonitor())
	case "vfkit":
		out = append(out, r.VFKitRestSock())
	}
	return out
}

// NetworkArtifacts returns the network-related socket paths (gvproxy plus driver sockets).
func (r RuntimeLayout) NetworkArtifacts() []string {
	return []string{r.NetworkSock(), r.QEMUNetSock(), r.VFKitNetSock()}
}
