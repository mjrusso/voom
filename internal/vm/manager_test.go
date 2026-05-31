package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/host"
	"github.com/mjrusso/voom/internal/share"
	"github.com/mjrusso/voom/internal/state"
)

func TestRequireGuestPortReportNeedsControlShare(t *testing.T) {
	im := &state.ImageRecord{Name: "nixos", Capabilities: state.ImageCapabilities{GuestPortReport: true}}
	if err := RequireGuestPortReport(im, "guest listener inspection"); err == nil {
		t.Fatal("expected controlShare capability gate")
	}
}

func TestSetResourcesUpdatesStoppedVM(t *testing.T) {
	st, _ := newTestStore(t)
	mgr := New(st)
	vmRec := &state.VMRecord{
		SchemaVersion: state.SchemaVersion,
		ID:            "vm1",
		Name:          "scratch",
		Driver:        "qemu",
		Resources:     state.VMResources{CPUs: 2, MemoryMiB: 512},
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := os.MkdirAll(st.VMDir(vmRec.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVM(vmRec); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(vmRec.Name, vmRec.ID); err != nil {
		t.Fatal(err)
	}

	updated, changed, err := mgr.SetCPUs("scratch", 4)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || updated.Resources.CPUs != 4 {
		t.Fatalf("SetCPUs changed=%t resources=%#v", changed, updated.Resources)
	}
	updated, changed, err = mgr.SetMemory("scratch", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || updated.Resources.MemoryMiB != 1024 {
		t.Fatalf("SetMemory changed=%t resources=%#v", changed, updated.Resources)
	}
	updated, changed, err = mgr.SetMemory("scratch", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if changed || updated.Resources.MemoryMiB != 1024 {
		t.Fatalf("idempotent SetMemory changed=%t resources=%#v", changed, updated.Resources)
	}
	reloaded, err := st.LoadVM("scratch")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Resources.CPUs != 4 || reloaded.Resources.MemoryMiB != 1024 {
		t.Fatalf("persisted resources = %#v", reloaded.Resources)
	}
	if _, _, err := mgr.SetCPUs("scratch", 0); err == nil || !strings.Contains(err.Error(), "at least 1") {
		t.Fatalf("expected CPU validation error, got %v", err)
	}
	if _, _, err := mgr.SetMemory("scratch", 0); err == nil || !strings.Contains(err.Error(), "at least 1MiB") {
		t.Fatalf("expected memory validation error, got %v", err)
	}
}

func TestSetResourcesRejectsRunningVM(t *testing.T) {
	st, _ := newTestStore(t)
	mgr := New(st)
	vmRec := &state.VMRecord{
		SchemaVersion: state.SchemaVersion,
		ID:            "vm1",
		Name:          "scratch",
		Driver:        "qemu",
		Resources:     state.VMResources{CPUs: 2, MemoryMiB: 512},
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := os.MkdirAll(st.VMDir(vmRec.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVM(vmRec); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(vmRec.Name, vmRec.ID); err != nil {
		t.Fatal(err)
	}
	cmd := fakeNamedVMProcess(t, "qemu-system-test")
	pidfile := st.Runtime(vmRec).VMPid()
	if err := os.MkdirAll(filepath.Dir(pidfile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := mgr.SetCPUs("scratch", 4); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("expected running CPU mutation rejection, got %v", err)
	}
	if _, _, err := mgr.SetMemory("scratch", 1024); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("expected running memory mutation rejection, got %v", err)
	}
	reloaded, err := st.LoadVM("scratch")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Resources.CPUs != 2 || reloaded.Resources.MemoryMiB != 512 {
		t.Fatalf("running VM resources changed: %#v", reloaded.Resources)
	}
}

func TestQEMUArgsIncludeSeedDiskAndVirtioFS(t *testing.T) {
	st, _ := newTestStore(t)
	mgr := New(st)
	vmRec := &state.VMRecord{
		ID:        "vm1",
		Name:      "scratch",
		Driver:    "qemu",
		Arch:      host.System(),
		Resources: state.VMResources{CPUs: 2, MemoryMiB: 512},
	}
	shares := []share.Runtime{{Tag: state.ControlShareTag, Sock: filepath.Join(st.Runtime(vmRec).Dir(), "virtiofs-"+state.ControlShareTag+".sock")}}
	args := mgr.qemuArgs(vmRec, shares)
	if !hasArgPair(args, "-drive", "file="+st.SeedImagePath(vmRec)+",if=virtio,format=raw,readonly=on") {
		t.Fatalf("missing seed drive: %#v", args)
	}
	if !hasArgPair(args, "-device", "vhost-user-fs-pci,chardev=chrfs0,tag="+state.ControlShareTag+",queue-size=1024") {
		t.Fatalf("missing control share device: %#v", args)
	}
}

func fakeNamedVMProcess(t *testing.T, name string, args ...string) *exec.Cmd {
	t.Helper()
	payload := "exec -a " + name + " bash -c 'trap \"exit 0\" TERM; while true; do sleep 1; done' " + stringsForShell(args)
	cmd := exec.Command("bash", "-c", payload)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := waitFor(context.Background(), func() bool {
		if runtime.GOOS == "linux" {
			cmdline, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "cmdline"))
			first := string(bytes.Split(cmdline, []byte{0})[0])
			return filepath.Base(first) == name
		}
		out, _ := exec.Command("ps", "-p", strconv.Itoa(cmd.Process.Pid), "-o", "command=").Output()
		return len(out) > 0 && filepath.Base(strings.Fields(string(out))[0]) == name
	}, time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("fake process did not assume name %s", name)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

func stringsForShell(args []string) string {
	out := ""
	for _, arg := range args {
		out += " '" + arg + "'"
	}
	return out
}

func TestWriteControlFilesIncludesMountsAndControlPaths(t *testing.T) {
	st, _ := newTestStore(t)
	mgr := New(st)
	vmRec := &state.VMRecord{
		ID:   "vm1",
		Name: "scratch",
		Shares: []share.Decl{
			{Tag: "src", HostPath: "/host/src", GuestPath: "/workspace/src", Readonly: true},
		},
	}
	if err := mgr.WriteControlFiles(vmRec); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(st.ControlShareDir(vmRec), "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Hostname     string `json:"hostname"`
		ControlShare struct {
			Tag        string `json:"tag"`
			GuestPath  string `json:"guestPath"`
			PortsPath  string `json:"portsPath"`
			MountsPath string `json:"mountsPath"`
		} `json:"controlShare"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ControlShare.Tag != state.ControlShareTag || payload.ControlShare.GuestPath != state.ControlShareGuestPath() || payload.ControlShare.MountsPath != filepath.Join(state.ControlShareGuestPath(), "mounts.json") {
		t.Fatalf("unexpected metadata payload: %#v", payload)
	}
	mountsRaw, err := os.ReadFile(st.ControlShareMountsPath(vmRec))
	if err != nil {
		t.Fatal(err)
	}
	var mounts []struct {
		Tag       string `json:"tag"`
		GuestPath string `json:"guestPath"`
		Readonly  bool   `json:"readonly"`
	}
	if err := json.Unmarshal(mountsRaw, &mounts); err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 || mounts[0].Tag != "src" || mounts[0].GuestPath != "/workspace/src" || !mounts[0].Readonly {
		t.Fatalf("unexpected mounts payload: %#v", mounts)
	}
}

func TestWriteSeedImageCreatesNoCloudDisk(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	t.Setenv("HOME", home)
	t.Setenv("VOOM_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("VOOM_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("VOOM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("VOOM_RUNTIME_DIR", filepath.Join(dir, "runtime"))
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519.pub"), []byte("ssh-ed25519 AAAATEST user@example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	mgr := New(st)
	im := &state.ImageRecord{
		SchemaVersion: state.SchemaVersion,
		ID:            "img1",
		Name:          "seedimg",
		Arch:          host.System(),
		Format:        "raw",
		Disk:          "disk.raw",
		Metadata:      state.ImageMetadata{SSHUser: "root", NixosTargetUser: "root"},
	}
	if err := os.MkdirAll(st.ImageDir(im.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(im.ID), "image.json"), im); err != nil {
		t.Fatal(err)
	}
	vmRec := &state.VMRecord{
		ID:    "vm1",
		Name:  "scratch",
		Image: state.VMImageRef{ID: im.ID, Name: im.Name},
		Access: state.VMAccess{
			SSHUser: "root",
		},
	}
	if err := os.MkdirAll(st.Runtime(vmRec).Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := mgr.WriteSeedImage(vmRec); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.SeedImagePath(vmRec)); err != nil {
		t.Fatalf("expected seed image at %s: %v", st.SeedImagePath(vmRec), err)
	}
}

func TestVFKitControlAndUserSharesDoNotRequireVirtiofsd(t *testing.T) {
	st, dir := newTestStore(t)
	hostShare := filepath.Join(dir, "share")
	if err := os.MkdirAll(hostShare, 0o755); err != nil {
		t.Fatal(err)
	}
	mgr := New(st)
	vmRec := &state.VMRecord{
		ID:     "vm1",
		Name:   "scratch",
		Driver: "vfkit",
		Shares: []share.Decl{
			{Tag: "src", HostPath: hostShare, GuestPath: "/workspace/src", Readonly: true},
		},
	}
	controlShare, err := mgr.StartControlShare(context.Background(), vmRec)
	if err != nil {
		t.Fatal(err)
	}
	if controlShare.Tag != state.ControlShareTag || controlShare.HostPath != st.ControlShareDir(vmRec) || controlShare.Sock != "" {
		t.Fatalf("unexpected vfkit control share runtime: %#v", controlShare)
	}
	userShares, err := mgr.StartUserShares(context.Background(), vmRec)
	if err != nil {
		t.Fatal(err)
	}
	if len(userShares) != 1 || userShares[0].Tag != "src" || userShares[0].HostPath != hostShare || !userShares[0].Readonly || userShares[0].Sock != "" {
		t.Fatalf("unexpected vfkit user shares: %#v", userShares)
	}
}

func TestReadGuestPortsRejectsMalformedAndStaleReports(t *testing.T) {
	st, _ := newTestStore(t)
	mgr := New(st)
	vmRec := &state.VMRecord{ID: "vm1", Name: "scratch"}
	reportDir := filepath.Join(st.Runtime(vmRec).Dir(), "control")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(reportDir, "ports.json")
	if err := os.WriteFile(reportPath, []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.ReadGuestPorts(vmRec); err == nil || !contains(err.Error(), "malformed") {
		t.Fatalf("expected malformed report rejection, got %v", err)
	}
	report := GuestPortsReport{
		SchemaVersion: 1,
		GeneratedAt:   time.Now().Add(-time.Minute).UTC(),
		Listeners:     []GuestListener{{Proto: "tcp", Addr: "0.0.0.0", Port: 8080}},
	}
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.ReadGuestPorts(vmRec); err == nil || !contains(err.Error(), "stale") {
		t.Fatalf("expected stale report rejection, got %v", err)
	}
}

func TestCreateUsesVFKitDiskExtension(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("vfkit driver is supported only on Apple Silicon macOS")
	}
	st, _ := newTestStore(t)

	im := &state.ImageRecord{
		SchemaVersion: state.SchemaVersion,
		ID:            "img1",
		Name:          "nixos",
		Arch:          host.System(),
		Format:        "raw",
		Disk:          "disk.raw",
		Metadata:      state.ImageMetadata{SSHUser: "root", NixosTargetUser: "root"},
	}
	if err := os.MkdirAll(st.ImageDir(im.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.ImageDiskPath(im), []byte("raw-image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(im.ID), "image.json"), im); err != nil {
		t.Fatal(err)
	}
	idx := st.IndexSnapshot()
	idx.Images[im.Name] = im.ID
	if err := st.SetIndexSnapshot(idx); err != nil {
		t.Fatal(err)
	}

	mgr := New(st)
	vmRec, err := mgr.Create(context.Background(), "scratch", im.Name, "vfkit", 2, 512, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.VMDiskPath(vmRec)); err != nil {
		t.Fatalf("expected vfkit VM disk at %s: %v", st.VMDiskPath(vmRec), err)
	}
	if _, err := os.Stat(filepath.Join(st.VMDir(vmRec.ID), "disk.raw")); !os.IsNotExist(err) {
		t.Fatalf("unexpected raw disk path for vfkit VM: %v", err)
	}
}

func TestCloneCopiesDiskAndResourcesButNotNetworkConfig(t *testing.T) {
	st, _ := newTestStore(t)
	mgr := New(st)
	src := &state.VMRecord{
		SchemaVersion: state.SchemaVersion,
		ID:            "vm1",
		Name:          "src",
		Driver:        "qemu",
		Arch:          host.System(),
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
		Image:         state.VMImageRef{ID: "img1", Name: "nixos"},
		Resources:     state.VMResources{CPUs: 3, MemoryMiB: 2048},
		Access:        state.VMAccess{SSHUser: "debian", NixosTargetUser: "debian"},
		Network: state.VMNetwork{
			SSHPort:               2222,
			SSHBind:               "127.0.0.1",
			Forwards:              []forward.Decl{{Protocol: "tcp", GuestPort: 8080, HostPort: 18080, Bind: "127.0.0.1"}},
			AutoForward:           true,
			AutoForwardHostOffset: 10000,
		},
		Shares: []share.Decl{{Tag: "code", HostPath: "/host/code", GuestPath: "/mnt/code", Readonly: true}},
	}
	if err := os.MkdirAll(st.VMDir(src.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.VMDiskPath(src), []byte("disk-state"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVM(src); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(src.Name, src.ID); err != nil {
		t.Fatal(err)
	}

	clone, err := mgr.Clone(context.Background(), "src", "dst")
	if err != nil {
		t.Fatal(err)
	}
	if clone.ID == src.ID {
		t.Fatalf("clone reused source ID %s", clone.ID)
	}
	if clone.Name != "dst" {
		t.Fatalf("clone name = %q", clone.Name)
	}
	if clone.Network.SSHPort == src.Network.SSHPort {
		t.Fatalf("clone reused source SSH port %d", clone.Network.SSHPort)
	}
	// Disk, resources, and access are carried; network config is not.
	if clone.Resources != src.Resources || clone.Access != src.Access {
		t.Fatalf("clone did not carry resources/access: %#v %#v", clone.Resources, clone.Access)
	}
	if clone.Network.AutoForward || clone.Network.AutoForwardHostOffset != 0 {
		t.Fatalf("clone should not carry auto-forward settings: %#v", clone.Network)
	}
	if len(clone.Network.Forwards) != 0 {
		t.Fatalf("clone should not carry forwards: %#v", clone.Network.Forwards)
	}
	if len(clone.Shares) != 0 {
		t.Fatalf("clone should not carry shares: %#v", clone.Shares)
	}
	disk, err := os.ReadFile(st.VMDiskPath(clone))
	if err != nil {
		t.Fatalf("reading clone disk: %v", err)
	}
	if string(disk) != "disk-state" {
		t.Fatalf("clone disk = %q, want copy of source disk", disk)
	}

	reloaded, err := st.LoadVM("dst")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ID != clone.ID {
		t.Fatalf("persisted clone ID = %s, want %s", reloaded.ID, clone.ID)
	}
	// The source's own config is untouched by the clone.
	srcReloaded, err := st.LoadVM("src")
	if err != nil {
		t.Fatal(err)
	}
	if len(srcReloaded.Shares) != 1 || len(srcReloaded.Network.Forwards) != 1 || !srcReloaded.Network.AutoForward {
		t.Fatalf("source config mutated by clone: %#v", srcReloaded)
	}

	if _, err := mgr.Clone(context.Background(), "src", "dst"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected duplicate-name rejection, got %v", err)
	}
}

func TestCloneRejectsRunningSource(t *testing.T) {
	st, _ := newTestStore(t)
	mgr := New(st)
	src := &state.VMRecord{
		SchemaVersion: state.SchemaVersion,
		ID:            "vm1",
		Name:          "src",
		Driver:        "qemu",
		Arch:          host.System(),
		Resources:     state.VMResources{CPUs: 2, MemoryMiB: 512},
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := os.MkdirAll(st.VMDir(src.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.VMDiskPath(src), []byte("disk-state"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVM(src); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(src.Name, src.ID); err != nil {
		t.Fatal(err)
	}
	cmd := fakeNamedVMProcess(t, "qemu-system-test")
	pidfile := st.Runtime(src).VMPid()
	if err := os.MkdirAll(filepath.Dir(pidfile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := mgr.Clone(context.Background(), "src", "dst"); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("expected running-source rejection, got %v", err)
	}
	if _, err := st.LoadVM("dst"); err == nil {
		t.Fatal("clone of running source should not have created a VM")
	}
}

func TestCloneWaitsForSourceLock(t *testing.T) {
	st, _ := newTestStore(t)
	mgr := New(st)
	src := &state.VMRecord{
		SchemaVersion: state.SchemaVersion,
		ID:            "vm1",
		Name:          "src",
		Driver:        "qemu",
		Arch:          host.System(),
		Resources:     state.VMResources{CPUs: 2, MemoryMiB: 512},
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := os.MkdirAll(st.VMDir(src.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.VMDiskPath(src), []byte("disk-state"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVM(src); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(src.Name, src.ID); err != nil {
		t.Fatal(err)
	}

	unlock, err := st.LockVM(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	cloneDone := make(chan error, 1)
	go func() {
		_, err := mgr.Clone(context.Background(), "src", "dst")
		cloneDone <- err
	}()
	select {
	case err := <-cloneDone:
		unlock()
		t.Fatalf("Clone returned while source lock was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-cloneDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Clone did not complete after source lock was released")
	}
}

func TestCloneCleansUpObjectDirectoryAfterCopyFailure(t *testing.T) {
	st, _ := newTestStore(t)
	mgr := New(st)
	src := &state.VMRecord{
		SchemaVersion: state.SchemaVersion,
		ID:            "vm1",
		Name:          "src",
		Driver:        "qemu",
		Arch:          host.System(),
		Resources:     state.VMResources{CPUs: 2, MemoryMiB: 512},
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := os.MkdirAll(st.VMDir(src.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.VMDiskPath(src), []byte("disk-state"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVM(src); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(src.Name, src.ID); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := mgr.Clone(ctx, "src", "dst"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Clone err = %v, want context.Canceled", err)
	}
	if _, err := st.LoadVM("dst"); err == nil {
		t.Fatal("failed clone should not have been registered")
	}
	entries, err := os.ReadDir(filepath.Join(st.Paths().State, "vms"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != src.ID {
		t.Fatalf("failed clone leaked VM object directory: %#v", entries)
	}
}

func contains(s, want string) bool {
	return strings.Contains(s, want)
}

func hasArgPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}
