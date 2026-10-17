package vm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/mjrusso/voom/internal/driver/qemu"
	"github.com/mjrusso/voom/internal/driver/vfkit"
	"github.com/mjrusso/voom/internal/egress"
	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
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
	if cpus == 0 {
		cpus = 4
		if im.MacOS != nil {
			cpus = im.MacOS.CPUs
		}
	}
	if mem == 0 {
		mem = 4096
		if im.MacOS != nil {
			mem = im.MacOS.MemoryMiB
		}
	}
	if cpus < 1 || mem < 1 {
		return nil, errors.New("CPU and memory allocations must be positive")
	}
	if im.MacOS != nil {
		if err := im.MacOS.ValidateResources(cpus, mem); err != nil {
			return nil, err
		}
		if err := im.MacOS.ValidateStorage(m.store.ImageDir(im.ID)); err != nil {
			return nil, err
		}
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
	if im.MacOS != nil {
		platform := *im.MacOS
		vm.MacOS = &platform
		vm.SchemaVersion = im.SchemaVersion
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
	cleanupObject := true
	defer func() {
		if cleanupObject {
			_ = os.RemoveAll(m.store.VMDir(id))
		}
	}()
	if im.MacOS != nil {
		vm.MacOS, err = im.MacOS.CopyVM(ctx, m.store.ImageDir(im.ID), m.store.VMDir(id))
	} else {
		err = state.CopyFile(ctx, m.store.ImageDiskPath(im), m.store.VMDiskPath(vm))
	}
	if err != nil {
		return nil, err
	}
	if err := m.store.WithGlobal(func() error {
		idx := m.store.IndexSnapshot()
		if idx.Images[imageName] != im.ID {
			return fmt.Errorf("image %q changed while creating VM", imageName)
		}
		if vm.MacOS != nil {
			if err := m.store.CheckMacOSIdentityAvailable(vm.MacOS); err != nil {
				return err
			}
		}
		var err error
		if sshPort == 0 {
			vm.Network.SSHPort, err = m.store.AllocateSSHPort()
		} else {
			err = m.store.EnsureSSHPortAvailable(sshPort)
		}
		if err != nil {
			return err
		}
		if err := m.store.SaveVM(vm); err != nil {
			return err
		}
		return m.store.RegisterVMLocked(name, id)
	}); err != nil {
		return nil, err
	}
	cleanupObject = false
	m.emitVM("create", vm, map[string]string{"image": im.Name, "driver": vm.Driver, "arch": vm.Arch})
	return vm, nil
}

// Clone provisions a new VM from an existing one by copying the source VM's
// current disk. The source VM must be stopped so the disk is captured in a
// consistent state. The clone receives a fresh ID and a newly-allocated SSH
// port and keeps the source's resources, access, and recorded NixOS switch
// metadata (all of which describe the copied disk). It does not inherit the
// source's shares, USB devices, manual forwards, or auto-forward settings:
// those carry host-side state (host ports, offsets, host paths) that would
// collide or surprise if duplicated silently. The 'voom clone' and
// 'voom config show' commands print the commands to reproduce them
// explicitly. The disk copy honors ctx cancellation.
func (m *Manager) Clone(ctx context.Context, srcName, dstName string) (_ *state.VMRecord, retErr error) {
	src, unlock, err := m.store.LockVMRecord(ctx, srcName)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if m.IsRunning(src) {
		return nil, fmt.Errorf("VM %q is running; stop it before cloning", srcName)
	}
	id, err := state.NewID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	clone := *src
	clone.ID = id
	clone.Name = dstName
	clone.CreatedAt = now
	clone.UpdatedAt = now
	clone.Shares = nil
	clone.USBDevices = nil
	clone.PCIDevices = nil
	clone.Network = state.VMNetwork{SSHBind: src.Network.SSHBind}
	if src.Nixos != nil {
		nixos := *src.Nixos
		clone.Nixos = &nixos
	}
	if err := os.MkdirAll(m.store.VMDir(id), 0o755); err != nil {
		return nil, err
	}
	cleanupObject := true
	defer func() {
		if !cleanupObject {
			return
		}
		if err := os.RemoveAll(m.store.VMDir(id)); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("cleaning up failed clone: %w", err))
		}
	}()
	if src.MacOS != nil {
		if err := state.WaitMacOSStorage(ctx, m.store.VMDir(src.ID)); err != nil {
			return nil, err
		}
		clone.MacOS, err = src.MacOS.CopyVM(ctx, m.store.VMDir(src.ID), m.store.VMDir(id))
	} else {
		err = state.CopyFile(ctx, m.store.VMDiskPath(src), m.store.VMDiskPath(&clone))
	}
	if err != nil {
		return nil, err
	}
	if err := m.store.WithGlobalVM(srcName, src.ID, func() error {
		if clone.MacOS != nil {
			if err := m.store.CheckMacOSIdentityAvailable(clone.MacOS); err != nil {
				return err
			}
		}
		port, err := m.store.AllocateSSHPort()
		if err != nil {
			return err
		}
		clone.Network.SSHPort = port
		if err := m.store.SaveVM(&clone); err != nil {
			return err
		}
		return m.store.RegisterVMLocked(dstName, id)
	}); err != nil {
		return nil, err
	}
	cleanupObject = false
	m.emitVM("create", &clone, map[string]string{
		"image": clone.Image.Name, "driver": clone.Driver, "arch": clone.Arch, "clonedFrom": src.Name,
	})
	return &clone, nil
}

// Remove stops the named VM if it is running and deletes its state.
func (m *Manager) Remove(ctx context.Context, name string) error {
	vm, unlock, err := m.store.LockVMRecord(ctx, name)
	if err != nil {
		return err
	}
	defer unlock()
	if hasRuntimeState(vm, m.store.Runtime(vm)) {
		if _, err := m.stopRuntime(ctx, vm); err != nil {
			return err
		}
	}
	if err := m.store.DeleteVM(vm); err != nil {
		return err
	}
	m.emitVM("rm", vm, nil)
	return nil
}

// GrowDisk expands the VM's disk image by the given size; the VM must be stopped.
func (m *Manager) GrowDisk(ctx context.Context, name, amount string) error {
	vm, unlock, err := m.store.LockVMRecord(ctx, name)
	if err != nil {
		return err
	}
	defer unlock()
	if vm.MacOS != nil {
		return errors.New("macOS disk growth is not supported")
	}
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
	vm, unlock, err := m.store.LockVMRecord(ctx, name)
	if err != nil {
		return err
	}
	defer unlock()
	if vm.MacOS != nil {
		return errors.New("macOS reset is not supported")
	}
	im, err := m.store.LoadImage(imageName)
	if err != nil {
		return err
	}
	if im.MacOS != nil {
		return errors.New("resetting to a macOS image is not supported")
	}
	if _, err := m.stopRuntime(ctx, vm); err != nil {
		return err
	}
	if err := host.ValidateHostImage(im.Arch, im.Format, vm.Driver); err != nil {
		return err
	}
	candidate := *vm
	candidate.Image = state.VMImageRef{ID: im.ID, Name: im.Name}
	candidate.Arch = im.Arch
	candidate.Access = state.VMAccess{SSHUser: im.Metadata.SSHUser, SSHIdentityPath: im.Metadata.SSHIdentityPath, NixosTargetUser: im.Metadata.NixosTargetUser}
	if candidate.Access.SSHUser == "" {
		candidate.Access.SSHUser = "root"
	}
	if candidate.Access.NixosTargetUser == "" {
		candidate.Access.NixosTargetUser = candidate.Access.SSHUser
	}
	disk := m.store.VMDiskPath(vm)
	stagedDisk := disk + ".reset"
	if err := state.CopyFile(ctx, m.store.ImageDiskPath(im), stagedDisk); err != nil {
		return err
	}
	defer func() { _ = os.Remove(stagedDisk) }()
	candidate.UpdatedAt = time.Now().UTC()
	if err := m.store.WithGlobalVM(name, vm.ID, func() error {
		if m.store.IndexSnapshot().Images[imageName] != im.ID {
			return fmt.Errorf("image %q changed while resetting VM", imageName)
		}
		if err := os.Rename(stagedDisk, disk); err != nil {
			return err
		}
		return m.store.SaveVM(&candidate)
	}); err != nil {
		return err
	}
	*vm = candidate
	return nil
}

// Start boots the named VM, launching gvproxy and the driver process and
// installing configured forwards; it returns the live record.
func (m *Manager) Start(ctx context.Context, stderr io.Writer, name string) (*state.VMRecord, error) {
	vm, unlock, err := m.store.LockVMRecord(ctx, name)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if vm.MacOS != nil {
		if err := vm.MacOS.ValidateStorage(m.store.VMDir(vm.ID)); err != nil {
			return nil, err
		}
	}
	if m.IsRunning(vm) {
		return vm, nil
	}
	if vm.Network.AutoForward {
		if err := ValidateAutoForwardConfig(vm); err != nil {
			return nil, err
		}
	}
	if d := vm.Network.Egress; d != nil {
		if err := m.store.WithGlobalVM(name, vm.ID, func() error {
			return m.CheckEgressAssignment(vm, *d)
		}); err != nil {
			return nil, err
		}
	}
	if _, err := m.stopRuntime(ctx, vm); err != nil {
		return nil, err
	}
	if err := host.ValidateHostImage(vm.Arch, state.ImageFormatFromDisk(m.store.VMDiskPath(vm)), vm.Driver); err != nil {
		return nil, err
	}
	if vm.MacOS != nil {
		if err := state.WaitMacOSStorage(ctx, m.store.VMDir(vm.ID)); err != nil {
			return nil, err
		}
	}
	requiredCapabilities := []string{gvproxy.GuestIsolationCapability}
	for _, f := range vm.Network.Forwards {
		if f.Protocol == "udp" {
			requiredCapabilities = append(requiredCapabilities, gvproxy.UDPForwardCapability)
			break
		}
	}
	var egressCA []byte
	if d := vm.Network.Egress; d != nil {
		if d.Enabled {
			egressCA, err = m.validateEgressTarget(ctx, vm)
			requiredCapabilities = append(requiredCapabilities, gvproxy.GatewayCapability)
		} else {
			err = egress.Syntax(*d)
		}
	}
	if err != nil {
		return nil, err
	}
	gvproxyExe, err := gvproxy.ResolveCompatible(ctx, requiredCapabilities...)
	if err != nil {
		return nil, err
	}
	switch vm.Driver {
	case "qemu":
		err = host.RequireExe(qemu.ExecutableName(vm.Arch))
		if err == nil && len(vm.USBDevices) > 0 {
			err = qemu.RequireUSB(vm.Arch)
		}
		if err == nil && len(vm.PCIDevices) > 0 {
			err = qemu.RequireVFIO(vm.Arch)
		}
		if err != nil {
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
	if vm.MacOS == nil && vm.Driver == "vfkit" && im.Metadata.KernelPath == "" {
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
	usbBindings, err := m.prepareUSBDevices(vm)
	if err != nil {
		return nil, err
	}
	if err := m.preparePCIDevices(vm); err != nil {
		return nil, err
	}
	if portBusy("tcp", vm.Network.SSHBind, vm.Network.SSHPort) {
		return nil, fmt.Errorf("persisted SSH port %s:%d is in use; stop the conflicting process or change VM state", vm.Network.SSHBind, vm.Network.SSHPort)
	}
	for _, f := range vm.Network.Forwards {
		if portBusy(f.Protocol, f.Bind, f.HostPort) {
			return nil, fmt.Errorf("declared %s forward %s is in use; remove the conflict or 'voom forward remove %s %d --protocol %s --bind %s'", f.Protocol, forward.Address(f.Bind, f.HostPort), vm.Name, f.HostPort, f.Protocol, f.Bind)
		}
	}
	firmwarePath := ""
	if vm.Driver == "qemu" && vm.Arch == "aarch64-linux" {
		firmwarePath, err = ResolveAarch64UEFI()
		if err != nil {
			return nil, err
		}
	}
	rt := m.store.Runtime(vm)
	cache := m.store.CacheVMDir(vm)
	if err := host.EnsureRuntimeDir(rt.Dir()); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return nil, err
	}
	for _, p := range rt.DriverSockets() {
		_ = os.Remove(p)
	}
	if vm.MacOS == nil {
		if err := m.WriteSeedImage(vm); err != nil {
			return nil, err
		}
		if err := m.WriteControlFiles(vm); err != nil {
			return nil, err
		}
	}
	if vm.Network.Egress.IsEnabled() {
		if _, err := publishEgress(rt, egressCA); err != nil {
			return nil, err
		}
	}
	// From here on, any error must tear down whatever partial runtime state
	// was allocated. A single deferred cleanup keeps the error paths terse
	// and surfaces cleanup failures via stderr.
	started := false
	defer func() {
		if started {
			return
		}
		if _, cerr := m.stopRuntime(ctx, vm); cerr != nil {
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
	gvproxyArgs := []string{"-listen", "unix://" + rt.NetworkSock(), "-ssh-port", "-1"}
	switch vm.Driver {
	case "qemu":
		gvproxyArgs = append(gvproxyArgs, "-listen-qemu", "unix://"+rt.QEMUNetSock())
	case "vfkit":
		gvproxyArgs = append(gvproxyArgs, "-listen-vfkit", "unixgram://"+rt.VFKitNetSock())
	}
	if vm.Network.Egress.IsEnabled() {
		gvproxyArgs = append(gvproxyArgs, "-gateway-forward", gvproxy.GatewayArgument(gvproxy.GatewayRoute{Local: egress.Listener, Target: vm.Network.Egress.BackendSocket}))
	}
	if err = process.StartRecorded(gvproxyExe, gvproxyArgs, m.store.LogPath(vm, "gvproxy"), rt.GVProxyProcessRecord()); err != nil {
		return nil, err
	}
	if err := waitFor(ctx, func() bool {
		_, running := process.ValidRecord(rt.GVProxyProcessRecord(), "gvproxy")
		return running && socketExists(rt.NetworkSock()) && socketExists(rt.DriverNetSock(vm.Driver))
	}, gvproxyStartTimeout); err != nil {
		return nil, fmt.Errorf("timed out waiting for gvproxy sockets; see %s", m.store.LogPath(vm, "gvproxy"))
	}
	if err := m.installDeclaredForwardsLocked(vm); err != nil {
		return nil, err
	}
	switch vm.Driver {
	case "qemu":
		qargs, err := m.qemuArgs(vm, firmwarePath, virtiofsShares, usbBindings)
		if err != nil {
			return nil, err
		}
		if err := m.withUSBStartReservation(ctx, vm, func() error {
			return m.startRecorded(qemu.ExecutableName(vm.Arch), qargs, m.store.LogPath(vm, "qemu"), rt.VMProcessRecord())
		}); err != nil {
			return nil, err
		}
		if err := waitFor(ctx, func() bool { return m.IsRunning(vm) && socketExists(rt.QEMUMonitor()) }, driverStartTimeout); err != nil {
			return nil, m.driverStartError(vm, "qemu")
		}
	case "vfkit":
		if err := m.startRecorded("vfkit", m.vfkitArgs(vm, im, virtiofsShares), m.store.LogPath(vm, "vfkit"), rt.VMProcessRecord()); err != nil {
			return nil, err
		}
		if err := waitFor(ctx, func() bool { return m.IsRunning(vm) && socketExists(rt.VFKitRestSock()) }, driverStartTimeout); err != nil {
			return nil, m.driverStartError(vm, "vfkit")
		}
	}
	if vm.Network.AutoForward && im.Capabilities.GuestPortReport && im.Capabilities.ControlShare {
		if err := m.StartAutoForwardWatcher(vm); err != nil {
			return nil, err
		}
	}
	started = true
	m.emitVM("start", vm, nil)
	_, _ = fmt.Fprintf(stderr, "VM %s started; SSH will become available at %s:%d after guest boots\n", vm.Name, vm.Network.SSHBind, vm.Network.SSHPort)
	return vm, nil
}

// Stop shuts down the named VM and tears down its runtime artifacts,
// returning whether any change occurred.
func (m *Manager) Stop(ctx context.Context, name string) (bool, error) {
	vm, unlock, err := m.store.LockVMRecord(ctx, name)
	if err != nil {
		return false, err
	}
	defer unlock()
	stop, err := m.stopLocked(ctx, vm)
	return stop.changed, err
}

// The caller must hold the VM lock.
func (m *Manager) stopLocked(ctx context.Context, vm *state.VMRecord) (runtimeStopResult, error) {
	stop, err := m.stopRuntime(ctx, vm)
	if err != nil {
		return stop, err
	}
	if stop.changed {
		m.emitVM("stop", vm, nil)
	}
	return stop, nil
}

type runtimeStopResult struct {
	changed                     bool
	gatewayTerminationConfirmed bool
}

type runtimeService struct {
	label     string
	kind      string
	record    string
	artifacts []string
	timeout   time.Duration
	logPath   string
}

func stopService(service runtimeService) (bool, error) {
	var failures []error
	if !process.HasRecord(service.record) {
		for _, path := range service.artifacts {
			if socketExists(path) {
				failures = append(failures, fmt.Errorf("%s termination unconfirmed: socket %s has no process identity", service.label, path))
			}
		}
	}
	if len(failures) == 0 {
		if err := process.StopRecorded(service.record, service.kind, service.timeout); err != nil {
			failures = append(failures, err)
		}
	}
	if err := errors.Join(failures...); err != nil {
		if service.logPath != "" {
			err = fmt.Errorf("%w; log: %s", err, service.logPath)
		}
		return false, err
	}
	for _, path := range service.artifacts {
		_ = os.Remove(path)
	}
	return true, nil
}

// stopRuntime requires the VM lock. It attempts every helper and retains records
// when termination cannot be confirmed. Gateway confirmation is independent of
// other cleanup errors because live egress rollback depends on it.
// Cancellation ends graceful waits; signal escalation has a separate time bound.
func (m *Manager) stopRuntime(ctx context.Context, vm *state.VMRecord) (runtimeStopResult, error) {
	changed := hasRuntimeState(vm, m.store.Runtime(vm))
	processErr := m.stopVMProcesses(ctx, vm)
	confirmed, gatewayErr := m.stopGVProxy(vm)
	filesChanged, cleanupErr := m.cleanupRuntime(vm, confirmed)
	return runtimeStopResult{
		changed:                     changed || filesChanged,
		gatewayTerminationConfirmed: confirmed,
	}, errors.Join(processErr, gatewayErr, cleanupErr)
}

func (m *Manager) stopVMProcesses(ctx context.Context, vm *state.VMRecord) error {
	rt := m.store.Runtime(vm)
	var failures []error
	if err := m.StopAutoForwardWatcher(vm); err != nil {
		failures = append(failures, err)
	}
	if pid, ok := process.ValidRecord(rt.VMProcessRecord(), state.VMProcessKind(vm.Driver)); ok {
		switch vm.Driver {
		case "qemu":
			if err := qemu.SystemPowerdown(ctx, rt.QEMUMonitor()); err == nil {
				_ = waitFor(ctx, func() bool { return !process.Alive(pid) }, driverStopTimeout)
			}
		case "vfkit":
			_ = vfkit.Stop(rt.VFKitRestSock(), false)
			timeout := driverStopTimeout
			if vm.MacOS != nil {
				timeout = 60 * time.Second
			}
			_ = waitFor(ctx, func() bool { return !process.Alive(pid) }, timeout)
		}
	}
	_, driverErr := stopService(runtimeService{
		label:     "VM",
		kind:      state.VMProcessKind(vm.Driver),
		record:    rt.VMProcessRecord(),
		artifacts: rt.DriverArtifacts(vm.Driver),
		timeout:   processStopTimeout,
	})
	if driverErr != nil {
		failures = append(failures, driverErr)
	}
	return errors.Join(failures...)
}

func (m *Manager) stopGVProxy(vm *state.VMRecord) (bool, error) {
	rt := m.store.Runtime(vm)
	return stopService(runtimeService{
		label: "gvproxy", kind: "gvproxy", record: rt.GVProxyProcessRecord(),
		artifacts: rt.NetworkArtifacts(), timeout: processStopTimeout,
		logPath: m.store.LogPath(vm, "gvproxy"),
	})
}

// The caller must hold the VM lock. Unconfirmed termination retains uncertain reservations.
func (m *Manager) cleanupRuntime(vm *state.VMRecord, gatewayConfirmed bool) (bool, error) {
	rt := m.store.Runtime(vm)
	var failures []error
	if gatewayConfirmed {
		if err := os.Remove(rt.ForwardMutation()); err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, err)
		}
	} else {
		operations := make([]forwardOperation, 0, len(vm.Network.Forwards))
		for _, f := range vm.Network.Forwards {
			operations = append(operations, forwardOperation{action: forwardUnexpose, endpoint: f})
		}
		if len(operations) > 0 {
			err := m.executeForwardMutation(vm, forwardMutation{operations: operations})
			failures = append(failures, err)
		}
	}
	var autoErr error
	if gatewayConfirmed {
		autoErr = m.clearRuntimeAutoForwardsLocked(vm)
	} else {
		autoErr = m.cleanupAutoForwardsLocked(vm)
	}
	failures = append(failures, autoErr)
	if err := m.stopVirtiofsd(rt); err != nil {
		failures = append(failures, err)
	}
	changed, err := removeEgressFiles(rt)
	return changed, errors.Join(append(failures, err)...)
}

func hasRuntimeState(vm *state.VMRecord, rt state.RuntimeLayout) bool {
	if process.HasRecord(rt.VMProcessRecord()) ||
		process.HasRecord(rt.GVProxyProcessRecord()) ||
		process.HasRecord(rt.AutoForwardProcessRecord()) ||
		len(rt.FindVirtiofsd()) > 0 {
		return true
	}
	paths := []string{rt.ForwardMutation(), rt.AutoForwardsJSON(), rt.EgressManifest(), rt.EgressCA()}
	paths = append(paths, rt.NetworkArtifacts()...)
	paths = append(paths, rt.DriverArtifacts(vm.Driver)...)
	for _, path := range paths {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	return false
}

// IsRunning reports whether the VM's driver process is alive.
func (m *Manager) IsRunning(vm *state.VMRecord) bool {
	_, ok := process.ValidRecord(m.store.Runtime(vm).VMProcessRecord(), state.VMProcessKind(vm.Driver))
	return ok
}

// Status returns "running" or "stopped" based on the supplied flag.
func Status(running bool) string {
	if running {
		return "running"
	}
	return "stopped"
}
