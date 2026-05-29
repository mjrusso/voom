package vm

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/mjrusso/voom/internal/driver/vfkit"
	"github.com/mjrusso/voom/internal/host"
	"github.com/mjrusso/voom/internal/image"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/share"
	"github.com/mjrusso/voom/internal/state"
)

// Create provisions a new VM record from the named image, allocating storage,
// an SSH port, and registering it in the store. The disk copy honors ctx
// cancellation.
func (m *Manager) Create(ctx context.Context, name, imageName, driver string, cpus, mem, sshPort int) (*state.VMRecord, error) {
	idx := m.store.IndexSnapshot()
	if _, ok := idx.VMs[name]; ok {
		return nil, fmt.Errorf("VM %q already exists", name)
	}
	im, err := m.store.LoadImage(imageName)
	if err != nil {
		return nil, err
	}
	if driver == "auto" || driver == "" {
		driver = state.DefaultDriver()
	}
	if err := host.ValidateHostImage(im.Arch, im.Format, driver); err != nil {
		return nil, err
	}
	if sshPort == 0 {
		allocated, err := m.store.AllocateSSHPort()
		if err != nil {
			return nil, err
		}
		sshPort = allocated
	} else if err := m.store.EnsureSSHPortAvailable(sshPort); err != nil {
		return nil, err
	}
	id, err := state.NewID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	vm := &state.VMRecord{
		SchemaVersion: state.SchemaVersion,
		ID:            id,
		Name:          name,
		Driver:        driver,
		Arch:          im.Arch,
		CreatedAt:     now,
		UpdatedAt:     now,
		Image:         state.VMImageRef{ID: im.ID, Name: im.Name},
		Resources:     state.VMResources{CPUs: cpus, MemoryMiB: mem},
		Access:        state.VMAccess{SSHUser: im.Metadata.SSHUser, SSHIdentityPath: im.Metadata.SSHIdentityPath, NixosTargetUser: im.Metadata.NixosTargetUser},
		Network:       state.VMNetwork{SSHPort: sshPort, SSHBind: "127.0.0.1"},
	}
	if vm.Access.SSHUser == "" {
		vm.Access.SSHUser = "root"
	}
	if vm.Access.NixosTargetUser == "" {
		vm.Access.NixosTargetUser = vm.Access.SSHUser
	}
	if err := os.MkdirAll(m.store.VMDir(id), 0o755); err != nil {
		return nil, err
	}
	if err := state.CopyFile(ctx, m.store.ImageDiskPath(im), m.store.VMDiskPath(vm)); err != nil {
		return nil, err
	}
	if err := m.store.SaveVM(vm); err != nil {
		return nil, err
	}
	return vm, m.store.RegisterVM(name, id)
}

// Remove stops the named VM if it is running and deletes its state.
func (m *Manager) Remove(ctx context.Context, name string) error {
	_, _ = m.Stop(ctx, name)
	_, err := m.store.DeleteVM(name)
	return err
}

// GrowDisk expands the VM's disk image by the given size; the VM must be stopped.
func (m *Manager) GrowDisk(ctx context.Context, name, amount string) error {
	vm, err := m.store.LoadVM(name)
	if err != nil {
		return err
	}
	unlock, err := m.store.LockVM(vm.ID)
	if err != nil {
		return err
	}
	defer unlock()
	if m.IsRunning(vm) {
		return fmt.Errorf("VM %q is running; stop it before growing its disk", name)
	}
	bytes, err := state.ParseSize(amount)
	if err != nil {
		return err
	}
	switch state.ImageFormatFromDisk(m.store.VMDiskPath(vm)) {
	case "raw":
		info, err := os.Stat(m.store.VMDiskPath(vm))
		if err != nil {
			return err
		}
		return os.Truncate(m.store.VMDiskPath(vm), info.Size()+bytes)
	default:
		return exec.CommandContext(ctx, "qemu-img", "resize", m.store.VMDiskPath(vm), "+"+strconv.FormatInt(bytes, 10)).Run()
	}
}

// ResetDisk replaces the VM's disk with a fresh copy of the given image,
// stopping the VM first if necessary.
func (m *Manager) ResetDisk(ctx context.Context, name, imageName string) error {
	_, _ = m.Stop(ctx, name)
	vm, err := m.store.LoadVM(name)
	if err != nil {
		return err
	}
	unlock, err := m.store.LockVM(vm.ID)
	if err != nil {
		return err
	}
	defer unlock()
	im, err := m.store.LoadImage(imageName)
	if err != nil {
		return err
	}
	if err := host.ValidateHostImage(im.Arch, im.Format, vm.Driver); err != nil {
		return err
	}
	oldDisk := m.store.VMDiskPath(vm)
	vm.Image = state.VMImageRef{ID: im.ID, Name: im.Name}
	vm.Arch = im.Arch
	vm.Access = state.VMAccess{SSHUser: im.Metadata.SSHUser, SSHIdentityPath: im.Metadata.SSHIdentityPath, NixosTargetUser: im.Metadata.NixosTargetUser}
	if vm.Access.SSHUser == "" {
		vm.Access.SSHUser = "root"
	}
	if vm.Access.NixosTargetUser == "" {
		vm.Access.NixosTargetUser = vm.Access.SSHUser
	}
	newDisk := m.store.VMDiskPath(vm)
	if err := state.CopyFile(ctx, m.store.ImageDiskPath(im), newDisk); err != nil {
		return err
	}
	if oldDisk != newDisk {
		_ = os.Remove(oldDisk)
	}
	vm.UpdatedAt = time.Now().UTC()
	return m.store.SaveVM(vm)
}

// Start boots the named VM, launching gvproxy and the driver process and
// installing configured forwards; it returns the live record.
func (m *Manager) Start(ctx context.Context, stderr io.Writer, name string) (*state.VMRecord, error) {
	vm, err := m.store.LoadVM(name)
	if err != nil {
		return nil, err
	}
	unlock, err := m.store.LockVM(vm.ID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if m.IsRunning(vm) {
		return vm, nil
	}
	if err := host.ValidateHostImage(vm.Arch, state.ImageFormatFromDisk(m.store.VMDiskPath(vm)), vm.Driver); err != nil {
		return nil, err
	}
	if err := host.RequireExe("gvproxy"); err != nil {
		return nil, err
	}
	switch vm.Driver {
	case "qemu":
		if err := host.RequireExe("qemu-system-" + strings.TrimSuffix(vm.Arch, "-linux")); err != nil {
			return nil, err
		}
		if err := host.RequireExe("qemu-img"); err != nil {
			return nil, err
		}
	case "vfkit":
		if err := host.RequireExe("vfkit"); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported driver %q", vm.Driver)
	}
	im, err := m.store.LoadImageByID(vm.Image.ID)
	if err != nil {
		return nil, err
	}
	if vm.Driver == "vfkit" && im.Metadata.KernelPath == "" {
		if err := image.ValidateVFKitBootDisk(m.store.VMDiskPath(vm)); err != nil {
			return nil, err
		}
	}
	if vm.Driver == "qemu" && (im.Capabilities.ControlShare || len(vm.Shares) > 0) {
		if err := host.RequireExe("virtiofsd"); err != nil {
			return nil, err
		}
	}
	if len(vm.Shares) > 0 && !im.Capabilities.ControlShare {
		return nil, fmt.Errorf("share mounting unavailable for image %q: controlShare capability is false; import an image with the reserved voom-control share enabled", im.Name)
	}
	if len(vm.Shares) > 0 && !im.Capabilities.GuestShareMount {
		return nil, fmt.Errorf("share mounting unavailable for image %q: guestShareMount capability is false; import an image with guest share-mount support", im.Name)
	}
	if portBusy(vm.Network.SSHBind, vm.Network.SSHPort) {
		return nil, fmt.Errorf("persisted SSH port %s:%d is in use; stop the conflicting process or change VM state", vm.Network.SSHBind, vm.Network.SSHPort)
	}
	for _, f := range vm.Network.Forwards {
		if portBusy(f.Bind, f.HostPort) {
			return nil, fmt.Errorf("declared forward %s:%d is in use; remove the conflict or 'voom forward rm %s %d'", f.Bind, f.HostPort, vm.Name, f.HostPort)
		}
	}
	uefi := ""
	if vm.Driver == "qemu" && strings.HasPrefix(vm.Arch, "aarch64") {
		uefi, err = ResolveAarch64UEFI()
		if err != nil {
			return nil, err
		}
	}
	rt := m.store.Runtime(vm)
	cache := m.store.CacheVMDir(vm)
	if err := os.MkdirAll(rt.Dir(), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return nil, err
	}
	for _, p := range rt.DriverSockets() {
		_ = os.Remove(p)
	}
	if err := m.WriteSeedImage(vm); err != nil {
		return nil, err
	}
	if err := m.WriteControlFiles(vm); err != nil {
		return nil, err
	}
	// From here on, any error must tear down whatever partial runtime state
	// was allocated. A single deferred cleanup keeps the error paths terse
	// and surfaces cleanup failures via stderr.
	started := false
	defer func() {
		if started {
			return
		}
		if cerr := m.StopRuntime(ctx, vm); cerr != nil {
			_, _ = fmt.Fprintf(stderr, "VM %s cleanup after failed start: %v\n", vm.Name, cerr)
		}
	}()
	virtiofsShares := []share.Runtime{}
	if im.Capabilities.ControlShare {
		controlShare, err := m.StartControlShare(ctx, vm)
		if err != nil {
			return nil, err
		}
		virtiofsShares = append(virtiofsShares, controlShare)
	}
	userShares, err := m.StartUserShares(ctx, vm)
	if err != nil {
		return nil, err
	}
	virtiofsShares = append(virtiofsShares, userShares...)
	gvproxyArgs := []string{"-listen", "unix://" + rt.NetworkSock(), "-ssh-port", "-1", "-pid-file", rt.GVProxyPid()}
	switch vm.Driver {
	case "qemu":
		gvproxyArgs = append(gvproxyArgs, "-listen-qemu", "unix://"+rt.QEMUNetSock())
	case "vfkit":
		gvproxyArgs = append(gvproxyArgs, "-listen-vfkit", "unixgram://"+rt.VFKitNetSock())
	}
	if _, err := m.startDetached("gvproxy", gvproxyArgs, m.store.LogPath(vm, "gvproxy")); err != nil {
		return nil, err
	}
	if err := waitFor(ctx, func() bool {
		return fileExists(rt.GVProxyPid()) && socketExists(rt.NetworkSock()) && socketExists(rt.DriverNetSock(vm.Driver))
	}, gvproxyStartTimeout); err != nil {
		return nil, fmt.Errorf("timed out waiting for gvproxy sockets; see %s", m.store.LogPath(vm, "gvproxy"))
	}
	guestTargetIP := m.GuestTargetIP(vm)
	if err := gvproxyExpose(rt.NetworkSock(), fmt.Sprintf("%s:%d", vm.Network.SSHBind, vm.Network.SSHPort), fmt.Sprintf("%s:22", guestTargetIP)); err != nil {
		return nil, fmt.Errorf("could not install SSH forward: %w", err)
	}
	for _, f := range vm.Network.Forwards {
		if err := gvproxyExpose(rt.NetworkSock(), fmt.Sprintf("%s:%d", f.Bind, f.HostPort), fmt.Sprintf("%s:%d", guestTargetIP, f.GuestPort)); err != nil {
			return nil, err
		}
	}
	switch vm.Driver {
	case "qemu":
		qemuBin := "qemu-system-" + strings.TrimSuffix(vm.Arch, "-linux")
		qargs := m.qemuArgs(vm, virtiofsShares)
		if strings.HasPrefix(vm.Arch, "aarch64") {
			qargs = append([]string{"-machine", "virt"}, qargs...)
			qargs = append(qargs, "-bios", uefi)
		}
		if _, err := m.startDetached(qemuBin, qargs, m.store.LogPath(vm, "qemu")); err != nil {
			return nil, err
		}
		if err := waitFor(ctx, func() bool { return m.IsRunning(vm) }, driverStartTimeout); err != nil {
			return nil, fmt.Errorf("qemu did not start; see %s", m.store.LogPath(vm, "qemu"))
		}
	case "vfkit":
		cmd, err := m.startDetached("vfkit", m.vfkitArgs(vm, im, virtiofsShares), m.store.LogPath(vm, "vfkit"))
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(rt.VMPid(), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
			return nil, err
		}
		if err := waitFor(ctx, func() bool { return m.IsRunning(vm) && socketExists(rt.VFKitRestSock()) }, driverStartTimeout); err != nil {
			return nil, fmt.Errorf("vfkit did not start; see %s", m.store.LogPath(vm, "vfkit"))
		}
	}
	if vm.Network.AutoForward && im.Capabilities.GuestPortReport && im.Capabilities.ControlShare {
		if err := ValidateAutoForwardConfig(vm); err != nil {
			return nil, err
		}
		if _, err := m.ReconcileAutoForwards(ctx, vm); err != nil {
			return nil, err
		}
		if err := m.StartAutoForwardWatcher(vm); err != nil {
			return nil, err
		}
	}
	started = true
	_, _ = fmt.Fprintf(stderr, "VM %s started; SSH will become available at %s:%d after guest boots\n", vm.Name, vm.Network.SSHBind, vm.Network.SSHPort)
	return vm, nil
}

// Stop shuts down the named VM and tears down its runtime artifacts,
// returning whether any change occurred.
func (m *Manager) Stop(ctx context.Context, name string) (bool, error) {
	vm, err := m.store.LoadVM(name)
	if err != nil {
		return false, err
	}
	unlock, err := m.store.LockVM(vm.ID)
	if err != nil {
		return false, err
	}
	defer unlock()
	rt := m.store.Runtime(vm)
	changed := m.IsRunning(vm) ||
		fileExists(rt.GVProxyPid()) ||
		fileExists(rt.AutoForwardPid()) ||
		fileExists(rt.AutoForwardsJSON()) ||
		len(globFiles(rt.VirtiofsPidGlob())) > 0
	if err := m.StopRuntime(ctx, vm); err != nil {
		return changed, err
	}
	return changed, nil
}

// StopRuntime terminates the VM's driver, gvproxy, virtiofsd, and auto-forward
// watcher processes and removes their runtime artifacts. The provided context
// bounds the graceful-shutdown waits; if ctx is already cancelled the waits
// return immediately and the underlying StopPidfile timeouts still kill the
// processes.
func (m *Manager) StopRuntime(ctx context.Context, vm *state.VMRecord) error {
	rt := m.store.Runtime(vm)
	m.StopAutoForwardWatcher(vm)
	_ = m.CleanupAutoForwards(ctx, vm)
	if pid, ok := validPid(rt.VMPid(), state.VMProcessKind(vm.Driver)); ok {
		switch vm.Driver {
		case "qemu":
			if conn, err := net.DialTimeout("unix", rt.QEMUMonitor(), time.Second); err == nil {
				_, _ = conn.Write([]byte("system_powerdown\n"))
				_ = conn.Close()
				_ = waitFor(ctx, func() bool { return !processAlive(pid) }, driverStopTimeout)
			}
		case "vfkit":
			_ = vfkit.Stop(rt.VFKitRestSock(), false)
			_ = waitFor(ctx, func() bool { return !processAlive(pid) }, driverStopTimeout)
		}
	}
	process.StopPidfile(rt.VMPid(), state.VMProcessKind(vm.Driver), processStopTimeout)
	for _, p := range rt.VMArtifacts(vm.Driver) {
		_ = os.Remove(p)
	}
	process.StopPidfile(rt.GVProxyPid(), "gvproxy", processStopTimeout)
	m.stopVirtiofsd(rt)
	for _, p := range rt.NetworkArtifacts() {
		_ = os.Remove(p)
	}
	return nil
}

// IsRunning reports whether the VM's driver process is alive.
func (m *Manager) IsRunning(vm *state.VMRecord) bool {
	_, ok := validPid(m.store.Runtime(vm).VMPid(), state.VMProcessKind(vm.Driver))
	return ok
}

// Status returns "running" or "stopped" based on the supplied flag.
func Status(running bool) string {
	if running {
		return "running"
	}
	return "stopped"
}
