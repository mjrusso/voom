package state

import (
	"path/filepath"
	"sort"
	"strings"
)

// RuntimeLayout resolves per-VM process record and socket paths under one base directory.
type RuntimeLayout struct {
	dir string
}

// VirtiofsdPaths contains the runtime paths for one virtiofsd instance.
type VirtiofsdPaths struct {
	Tag           string
	ProcessRecord string
	Socket        string
	LockFile      string
}

// Runtime returns the RuntimeLayout rooted at the VM's runtime directory.
func (s *Store) Runtime(vm *VMRecord) RuntimeLayout {
	return RuntimeLayout{dir: s.RuntimeVMDir(vm)}
}

// Dir returns the runtime base directory.
func (r RuntimeLayout) Dir() string {
	return r.dir
}

// VMProcessRecord returns the VM process record path.
func (r RuntimeLayout) VMProcessRecord() string {
	return filepath.Join(r.dir, "vm.process.json")
}

// GVProxyProcessRecord returns the gvproxy process record path.
func (r RuntimeLayout) GVProxyProcessRecord() string {
	return filepath.Join(r.dir, "gvproxy.process.json")
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

// AutoForwardProcessRecord returns the auto-forward watcher process record path.
func (r RuntimeLayout) AutoForwardProcessRecord() string {
	return filepath.Join(r.dir, "auto-forward.process.json")
}

// AutoForwardsJSON returns the path to the auto-forward runtime state JSON.
func (r RuntimeLayout) AutoForwardsJSON() string {
	return filepath.Join(r.dir, "auto-forwards.json")
}

// Virtiofsd returns the runtime paths for a share tag.
func (r RuntimeLayout) Virtiofsd(tag string) VirtiofsdPaths {
	base := filepath.Join(r.dir, "virtiofs-"+tag)
	return VirtiofsdPaths{
		Tag:           tag,
		ProcessRecord: base + ".process.json",
		Socket:        base + ".sock",
		LockFile:      base + ".sock.pid",
	}
}

// FindVirtiofsd discovers virtiofsd instances from their runtime artifacts.
func (r RuntimeLayout) FindVirtiofsd() []VirtiofsdPaths {
	tags := map[string]struct{}{}
	for _, suffix := range []string{".process.json", ".sock", ".sock.pid"} {
		matches, _ := filepath.Glob(filepath.Join(r.dir, "virtiofs-*"+suffix))
		for _, path := range matches {
			name := filepath.Base(path)
			tag := strings.TrimSuffix(strings.TrimPrefix(name, "virtiofs-"), suffix)
			if tag != "" {
				tags[tag] = struct{}{}
			}
		}
	}
	ordered := make([]string, 0, len(tags))
	for tag := range tags {
		ordered = append(ordered, tag)
	}
	sort.Strings(ordered)
	paths := make([]VirtiofsdPaths, 0, len(ordered))
	for _, tag := range ordered {
		paths = append(paths, r.Virtiofsd(tag))
	}
	return paths
}

// DriverSockets returns the union of driver and network socket paths used by both drivers.
func (r RuntimeLayout) DriverSockets() []string {
	return []string{r.NetworkSock(), r.QEMUNetSock(), r.QEMUMonitor(), r.VFKitNetSock(), r.VFKitRestSock()}
}

// DriverArtifacts returns the driver-specific control endpoint paths for a running VM.
func (r RuntimeLayout) DriverArtifacts(driver string) []string {
	var out []string
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

// EgressManifest returns the host-side guest publication marker.
func (r RuntimeLayout) EgressManifest() string { return filepath.Join(r.dir, "control", "egress.json") }

// EgressCA returns the public certificate copy in the control share.
func (r RuntimeLayout) EgressCA() string { return filepath.Join(r.dir, "control", "egress-ca.pem") }
