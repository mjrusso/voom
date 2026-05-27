package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mjrusso/voom/internal/host"
)

func TestOpenRejectsUnsupportedSchema(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("VOOM_STATE_DIR", dir)
	t.Setenv("VOOM_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("VOOM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("VOOM_RUNTIME_DIR", filepath.Join(dir, "runtime"))
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"schemaVersion":2,"vms":{},"images":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(); err == nil || !strings.Contains(err.Error(), "unsupported state schema version 2") {
		t.Fatalf("expected schema rejection, got %v", err)
	}
}

func TestParseCapabilitiesSupportsNestedAndTopLevel(t *testing.T) {
	caps := ParseCapabilities(map[string]any{"capabilities": map[string]any{"metadataDisk": true, "guestPortReport": true}})
	if !caps.MetadataDisk || !caps.GuestPortReport || caps.ControlShare {
		t.Fatalf("unexpected nested caps: %#v", caps)
	}
	caps = ParseCapabilities(map[string]any{"metadataDisk": true, "controlShare": true, "guestPortReport": true, "guestShareMount": true, "nixosSwitch": true})
	if !caps.MetadataDisk || !caps.ControlShare || !caps.GuestPortReport || !caps.GuestShareMount || !caps.NixosSwitch {
		t.Fatalf("unexpected top-level caps: %#v", caps)
	}
}

func TestParseKernelCmdlineSupportsBootspecShapes(t *testing.T) {
	params := ParseKernelCmdline(map[string]any{
		"kernelParams": []any{"console=ttyS0", "init=/nix/store/init"},
	})
	if got := strings.Join(params, " "); got != "console=ttyS0 init=/nix/store/init" {
		t.Fatalf("unexpected top-level kernelParams: %q", got)
	}

	params = ParseKernelCmdline(map[string]any{
		"org.nixos.bootspec.v1": map[string]any{
			"kernelParams": []any{"console=ttyS0", "loglevel=4"},
		},
	})
	if got := strings.Join(params, " "); got != "console=ttyS0 loglevel=4" {
		t.Fatalf("unexpected bootspec kernelParams: %q", got)
	}
}

func TestImportImageParsesBootspecMetadata(t *testing.T) {
	store, dir := newTestStore(t)

	imagePath := filepath.Join(dir, "golden.raw")
	if err := os.WriteFile(imagePath, []byte("raw-image"), 0o644); err != nil {
		t.Fatal(err)
	}
	metaPath := filepath.Join(dir, "golden.raw.meta.json")
	meta := map[string]any{
		"user":   "root",
		"system": host.System(),
		"format": "raw",
		"org.nixos.bootspec.v1": map[string]any{
			"kernel":       "/nix/store/kernel/Image",
			"initrd":       "/nix/store/initrd/initrd",
			"init":         "/nix/store/system/init",
			"kernelParams": []string{"console=ttyS0", "loglevel=4"},
		},
	}
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath, b, 0o644); err != nil {
		t.Fatal(err)
	}

	im, err := store.ImportImage(context.Background(), ImportOptions{Name: "golden", Src: imagePath, MetaPath: metaPath})
	if err != nil {
		t.Fatal(err)
	}
	if im.Metadata.KernelPath != "/nix/store/kernel/Image" {
		t.Fatalf("kernelPath = %q", im.Metadata.KernelPath)
	}
	if im.Metadata.InitrdPath != "/nix/store/initrd/initrd" {
		t.Fatalf("initrdPath = %q", im.Metadata.InitrdPath)
	}
	if im.Metadata.InitPath != "/nix/store/system/init" {
		t.Fatalf("initPath = %q", im.Metadata.InitPath)
	}
	if got := strings.Join(im.Metadata.KernelCmdline, " "); got != "console=ttyS0 loglevel=4" {
		t.Fatalf("kernelCmdline = %q", got)
	}
}

func TestDiskExtensionUsesImgForVFKit(t *testing.T) {
	if got := DiskExtension("vfkit"); got != "img" {
		t.Fatalf("DiskExtension(vfkit) = %q, want img", got)
	}
	if got := DiskExtension("qemu"); got != "qcow2" {
		t.Fatalf("DiskExtension(qemu) = %q, want qcow2", got)
	}
}

func TestRuntimeLayoutPaths(t *testing.T) {
	st, dir := newTestStore(t)
	vm := &VMRecord{ID: "vm1", Driver: "qemu"}
	rt := st.Runtime(vm)
	wantDir := filepath.Join(dir, "runtime", "vms", "vm1")
	if rt.Dir() != wantDir {
		t.Fatalf("Runtime.Dir() = %q, want %q", rt.Dir(), wantDir)
	}
	checks := map[string]string{
		rt.VMPid():            filepath.Join(wantDir, "vm.pid"),
		rt.GVProxyPid():       filepath.Join(wantDir, "gvproxy.pid"),
		rt.NetworkSock():      filepath.Join(wantDir, "network.sock"),
		rt.QEMUNetSock():      filepath.Join(wantDir, "qemu-net.sock"),
		rt.VFKitNetSock():     filepath.Join(wantDir, "vfkit-net.sock"),
		rt.QEMUMonitor():      filepath.Join(wantDir, "qemu.mon"),
		rt.VFKitRestSock():    filepath.Join(wantDir, "vfkit.sock"),
		rt.EFIStore():         filepath.Join(wantDir, "efi-variable-store"),
		rt.AutoForwardPid():   filepath.Join(wantDir, "auto-forward.pid"),
		rt.AutoForwardsJSON(): filepath.Join(wantDir, "auto-forwards.json"),
		rt.VirtiofsPidGlob():  filepath.Join(wantDir, "virtiofs-*.pid"),
		rt.VirtiofsSockGlob(): filepath.Join(wantDir, "virtiofs-*.sock"),
	}
	for got, want := range checks {
		if got != want {
			t.Fatalf("runtime path = %q, want %q", got, want)
		}
	}
	if rt.DriverNetSock("qemu") != rt.QEMUNetSock() {
		t.Fatalf("qemu driver socket = %q, want %q", rt.DriverNetSock("qemu"), rt.QEMUNetSock())
	}
	if rt.DriverNetSock("vfkit") != rt.VFKitNetSock() {
		t.Fatalf("vfkit driver socket = %q, want %q", rt.DriverNetSock("vfkit"), rt.VFKitNetSock())
	}
}

func TestImportImageRequiresSSHUser(t *testing.T) {
	store, dir := newTestStore(t)
	imagePath := filepath.Join(dir, "blank.raw")
	if err := os.WriteFile(imagePath, []byte("raw-image"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ImportImage(context.Background(), ImportOptions{Name: "blank", Src: imagePath, Arch: host.System(), Format: "raw"}); err == nil || !strings.Contains(err.Error(), "--ssh-user is required") {
		t.Fatalf("expected ssh-user required error, got %v", err)
	}

	im, err := store.ImportImage(context.Background(), ImportOptions{Name: "blank", Src: imagePath, Arch: host.System(), Format: "raw", SSHUser: "debian"})
	if err != nil {
		t.Fatal(err)
	}
	if im.Metadata.SSHUser != "debian" {
		t.Fatalf("SSHUser = %q, want debian", im.Metadata.SSHUser)
	}
	if im.Metadata.NixosTargetUser != "debian" {
		t.Fatalf("NixosTargetUser = %q, want debian", im.Metadata.NixosTargetUser)
	}
	if im.Capabilities.ControlShare || im.Capabilities.GuestPortReport || im.Capabilities.GuestShareMount || im.Capabilities.NixosSwitch {
		t.Fatalf("expected all capabilities false without sidecar, got %#v", im.Capabilities)
	}
}

func TestImportImageFlagOverridesSidecarUser(t *testing.T) {
	store, dir := newTestStore(t)

	imagePath := filepath.Join(dir, "image.raw")
	if err := os.WriteFile(imagePath, []byte("raw-image"), 0o644); err != nil {
		t.Fatal(err)
	}
	metaPath := filepath.Join(dir, "image.raw.meta.json")
	meta := map[string]any{"user": "root", "system": host.System(), "format": "raw"}
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath, b, 0o644); err != nil {
		t.Fatal(err)
	}

	im, err := store.ImportImage(context.Background(), ImportOptions{Name: "img", Src: imagePath, MetaPath: metaPath, SSHUser: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if im.Metadata.SSHUser != "operator" {
		t.Fatalf("SSHUser = %q, want operator (flag should override sidecar)", im.Metadata.SSHUser)
	}
}

func TestImportImageInstallGuestHelpersFlipsCapabilities(t *testing.T) {
	store, dir := newTestStore(t)
	imagePath := filepath.Join(dir, "image.raw")
	if err := os.WriteFile(imagePath, []byte("raw-image"), 0o644); err != nil {
		t.Fatal(err)
	}

	im, err := store.ImportImage(context.Background(), ImportOptions{Name: "via-flag", Src: imagePath, Arch: host.System(), Format: "raw", SSHUser: "debian", InstallGuestHelpers: true})
	if err != nil {
		t.Fatal(err)
	}
	if !im.Metadata.InstallGuestHelpers {
		t.Fatalf("InstallGuestHelpers should be true via flag")
	}
	if !im.Capabilities.ControlShare || !im.Capabilities.GuestPortReport || !im.Capabilities.GuestShareMount {
		t.Fatalf("expected three caps true with guest helpers, got %#v", im.Capabilities)
	}
	if im.Capabilities.NixosSwitch {
		t.Fatalf("guest helpers must not enable nixosSwitch")
	}

	metaPath := filepath.Join(dir, "image.raw.meta.json")
	if err := os.WriteFile(metaPath, []byte(`{"system":"`+host.System()+`","format":"raw","user":"debian","installGuestHelpers":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	im2, err := store.ImportImage(context.Background(), ImportOptions{Name: "via-sidecar", Src: imagePath, MetaPath: metaPath})
	if err != nil {
		t.Fatal(err)
	}
	if !im2.Metadata.InstallGuestHelpers || !im2.Capabilities.ControlShare {
		t.Fatalf("sidecar installGuestHelpers: true should imply InstallGuestHelpers + caps, got meta=%#v caps=%#v", im2.Metadata, im2.Capabilities)
	}
}

func TestEnsureSSHPortAvailableRejectsBusyAndInvalidPorts(t *testing.T) {
	busy := SSHLow
	store, _ := newTestStore(t, WithHostPortAvailable(func(_ string, port int) (bool, string) {
		return port != busy, "host port is already in use"
	}))

	if err := store.EnsureSSHPortAvailable(0); err == nil || !strings.Contains(err.Error(), "invalid SSH port") {
		t.Fatalf("expected invalid-port error, got %v", err)
	}
	if err := store.EnsureSSHPortAvailable(70000); err == nil || !strings.Contains(err.Error(), "invalid SSH port") {
		t.Fatalf("expected invalid-port error, got %v", err)
	}

	if err := store.EnsureSSHPortAvailable(busy); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("expected OS-busy error for port %d, got %v", busy, err)
	}

	idx := store.IndexSnapshot()
	idx.VMs["taken"] = "VM_RESERVED"
	if err := store.SetIndexSnapshot(idx); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(store.VMDir("VM_RESERVED"), 0o755); err != nil {
		t.Fatal(err)
	}
	reservedVM := &VMRecord{SchemaVersion: SchemaVersion, ID: "VM_RESERVED", Name: "taken", Driver: "qemu", Network: VMNetwork{SSHPort: SSHHigh, SSHBind: "127.0.0.1"}}
	if err := store.SaveVM(reservedVM); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSSHPortAvailable(SSHHigh); err == nil || !strings.Contains(err.Error(), "already used by another voom VM") {
		t.Fatalf("expected reserved-by-VM error, got %v", err)
	}
}

func TestAllocateSSHPortSkipsOSBusyPorts(t *testing.T) {
	store, _ := newTestStore(t, WithHostPortAvailable(func(_ string, port int) (bool, string) {
		return port != SSHLow, "host port is already in use"
	}))

	got, err := store.AllocateSSHPort()
	if err != nil {
		t.Fatalf("AllocateSSHPort returned error: %v", err)
	}
	if got != SSHLow+1 {
		t.Fatalf("AllocateSSHPort = %d, want %d", got, SSHLow+1)
	}
}

func TestAllocateSSHPortErrorsWhenAllPortsUnavailable(t *testing.T) {
	store, _ := newTestStore(t, WithHostPortAvailable(func(string, int) (bool, string) {
		return false, "host port is already in use"
	}))
	if _, err := store.AllocateSSHPort(); err == nil || !strings.Contains(err.Error(), "no free SSH port") {
		t.Fatalf("expected no-free-port error, got %v", err)
	}
}

func TestDefaultGuestTargetIPUsesGVProxyGuestAddress(t *testing.T) {
	if got := DefaultGuestTargetIP("qemu"); got != "192.168.127.3" {
		t.Fatalf("DefaultGuestTargetIP(qemu) = %q", got)
	}
	if got := DefaultGuestTargetIP("vfkit"); got != "192.168.127.3" {
		t.Fatalf("DefaultGuestTargetIP(vfkit) = %q", got)
	}
}

func TestGuestMACUsesDeterministicLocalAdminAddress(t *testing.T) {
	if got := GuestMAC("qemu", "vm-seed"); !strings.HasPrefix(got, "52:54:") {
		t.Fatalf("GuestMAC(qemu) = %q", got)
	}
	if got := GuestMAC("vfkit", "vm-seed"); !strings.HasPrefix(got, "02:") {
		t.Fatalf("GuestMAC(vfkit) = %q", got)
	}
}

func TestCopyFileCopiesContentAndNormalizesMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.img")
	dst := filepath.Join(dir, "nested", "dst.img")
	payload := bytes.Repeat([]byte("voom"), 4096)
	if err := os.WriteFile(src, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CopyFile(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("copied payload mismatch: got %d bytes want %d", len(got), len(payload))
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if perms := info.Mode().Perm(); perms != 0o644 {
		t.Fatalf("copied mode = %o, want 644", perms)
	}
}

func TestCopyFileProducesIndependentDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.img")
	dst := filepath.Join(dir, "dst.img")
	if err := os.WriteFile(src, []byte("source-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CopyFile(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("dest-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcBytes, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dstBytes, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(srcBytes) != "source-data" {
		t.Fatalf("source mutated after destination write: %q", srcBytes)
	}
	if string(dstBytes) != "dest-data" {
		t.Fatalf("destination write missing: %q", dstBytes)
	}
}

func TestTryCloneCopyCreatesDestinationWhenSupported(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.img")
	dst := filepath.Join(dir, "dst.img")
	if err := os.WriteFile(src, []byte("clone me"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := tryCloneCopy(context.Background(), src, dst)
	if err != nil {
		t.Skipf("platform clone helper unavailable here (%s/%s): %v", runtime.GOOS, runtime.GOARCH, err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "clone me" {
		t.Fatalf("clone output mismatch: %q", got)
	}
}

func TestCopyFileFallsBackWhenCloneProducesWrongSize(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.img")
	dst := filepath.Join(dir, "dst.img")
	payload := bytes.Repeat([]byte("voom"), 2048)
	if err := os.WriteFile(src, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	origCloneCopy := cloneCopy
	t.Cleanup(func() { cloneCopy = origCloneCopy })
	cloneCopy = func(_ context.Context, _, dst string) error {
		return os.WriteFile(dst, []byte("short"), 0o644)
	}

	if err := CopyFile(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("CopyFile did not fall back after bad clone result")
	}
}

func TestCopyFileHonorsCancelledContext(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.img")
	dst := filepath.Join(dir, "dst.img")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	origCloneCopy := cloneCopy
	t.Cleanup(func() { cloneCopy = origCloneCopy })
	cloneCopy = func(ctx context.Context, _, dst string) error {
		// Mimic a partially-written .partial file from a cancelled cp / clone.
		_ = os.WriteFile(dst, []byte("partial"), 0o644)
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := CopyFile(ctx, src, dst)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CopyFile err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(dst + ".partial"); !os.IsNotExist(err) {
		t.Fatalf(".partial leaked after cancel: stat err = %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("dst should not exist after cancel: stat err = %v", err)
	}
}

func TestCopyFileCancelInterruptsStreamingFallback(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.img")
	dst := filepath.Join(dir, "dst.img")
	// Large enough payload that io.Copy makes multiple Read calls.
	if err := os.WriteFile(src, bytes.Repeat([]byte("x"), 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}

	origCloneCopy := cloneCopy
	t.Cleanup(func() { cloneCopy = origCloneCopy })
	// Force the streaming fallback by failing the clone path.
	cloneCopy = func(_ context.Context, _, _ string) error {
		return errors.New("forced fallback")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := CopyFile(ctx, src, dst)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CopyFile err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(dst + ".partial"); !os.IsNotExist(err) {
		t.Fatalf(".partial leaked after streaming cancel: stat err = %v", err)
	}
}
