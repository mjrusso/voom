package vm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/share"
	"github.com/mjrusso/voom/internal/state"
)

func TestMacOSGuestServicesShareAndAutoForwardDeclarations(t *testing.T) {
	st, vm, im := macOSResetFixture(t)
	im.Capabilities = state.ImageCapabilities{ControlShare: true, GuestShareMount: true, GuestPortReport: true}
	if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(im.ID), "image.json"), im); err != nil {
		t.Fatal(err)
	}
	vm.Image = state.VMImageRef{ID: im.ID, Name: im.Name}
	if err := st.SaveVM(vm); err != nil {
		t.Fatal(err)
	}
	manager := New(st)
	ctx := context.Background()
	decl, err := manager.AddShare(ctx, vm.Name, "src", t.TempDir(), "/Volumes/source files", true)
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	vm, err = manager.UpdateAutoForward(ctx, vm.Name, AutoForwardUpdate{Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	if !vm.Network.AutoForward || len(vm.Shares) != 1 || vm.Shares[0] != decl {
		t.Fatalf("guest service declarations were not saved: %+v", vm)
	}
	if err := os.MkdirAll(st.ControlShareDir(vm), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.ControlSharePortsPath(vm), []byte("stale listener report"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.prepareGuestFiles(vm, im); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.ControlSharePortsPath(vm)); !os.IsNotExist(err) {
		t.Fatalf("stale port report survived guest preparation: %v", err)
	}
	if _, err := os.Stat(st.SeedImagePath(vm)); !os.IsNotExist(err) {
		t.Fatalf("macOS guest preparation created a Linux seed disk: %v", err)
	}
	var mounts []share.Decl
	if err := state.ReadJSON(st.ControlShareMountsPath(vm), &mounts); err != nil || len(mounts) != 1 || mounts[0].GuestPath != decl.GuestPath || !mounts[0].Readonly {
		t.Fatalf("mount contract: %+v %v", mounts, err)
	}
	var metadata struct {
		ControlShare struct{ GuestPath, PortsPath, MountsPath string }
	}
	if err := state.ReadJSON(filepath.Join(st.ControlShareDir(vm), "metadata.json"), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.ControlShare.GuestPath != "/var/run/voom" || metadata.ControlShare.PortsPath != "/var/run/voom/ports.json" {
		t.Fatalf("Darwin control paths: %+v", metadata)
	}
	control, err := manager.StartControlShare(ctx, vm)
	if err != nil {
		t.Fatal(err)
	}
	shares, err := manager.StartUserShares(ctx, vm)
	if err != nil {
		t.Fatal(err)
	}
	vargs, err := manager.vfkitArgs(vm, im, append([]share.Runtime{control}, shares...))
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(vargs, " ")
	for _, wanted := range []string{"mountTag=voom-control", "mountTag=src,readonly", "sharedDir=" + decl.HostPath} {
		if !strings.Contains(args, wanted) {
			t.Fatalf("missing %q from macOS share arguments: %s", wanted, args)
		}
	}
	if strings.Contains(args, "seed.img") || strings.Contains(args, "virtio-serial") {
		t.Fatalf("Linux devices leaked into macOS boot: %s", args)
	}
	if err := manager.RemoveShare(ctx, vm.Name, "src"); err != nil {
		t.Fatal(err)
	}
}

func TestMacOSResetRejectsMissingGuestServicesBeforeStoppingRuntime(t *testing.T) {
	for _, feature := range []string{"share", "auto-forward"} {
		t.Run(feature, func(t *testing.T) {
			st, vm, image := macOSResetFixture(t)
			if feature == "share" {
				vm.Shares = []share.Decl{{Tag: "src", HostPath: t.TempDir(), GuestPath: "/Volumes/src"}}
			} else {
				vm.Network.AutoForward = true
			}
			if err := st.SaveVM(vm); err != nil {
				t.Fatal(err)
			}
			driver := fakeNamedVMProcess(t, "vfkit")
			writeProcessRecord(t, st.Runtime(vm).VMProcessRecord(), driver)
			manager := New(st)
			if err := manager.ResetDisk(context.Background(), vm.Name, image.Name); err == nil || !strings.Contains(err.Error(), "capabilit") {
				t.Fatalf("incompatible reset accepted: %v", err)
			}
			if !manager.IsRunning(vm) {
				t.Fatal("incompatible reset stopped the VM")
			}
			if _, err := os.Stat(st.VMDiskPath(vm)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMacOSAutoForwardReconcilesGuestListenerReports(t *testing.T) {
	st, manager, vm, proxy := newAutoForwardFixture(t)
	platform := &state.MacOSPlatform{IdentitySHA256: strings.Repeat("a", 64), CPUs: 4, MemoryMiB: 8192, Version: "27.0.1", Build: "26A434"}
	image, err := st.LoadImageByID(vm.Image.ID)
	if err != nil {
		t.Fatal(err)
	}
	image.SchemaVersion, image.Arch, image.Format, image.Disk, image.MacOS = 2, "aarch64-darwin", "raw", "disk.raw", platform
	if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(image.ID), "image.json"), image); err != nil {
		t.Fatal(err)
	}
	vm.StorageGeneration = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	vm.SchemaVersion, vm.Arch, vm.Driver, vm.MacOS = 2, "aarch64-darwin", "vfkit", platform
	vm.Resources = state.VMResources{CPUs: 4, MemoryMiB: 8192}
	if err := st.SaveVM(vm); err != nil {
		t.Fatal(err)
	}
	writeProcessRecord(t, st.Runtime(vm).VMProcessRecord(), fakeNamedVMProcess(t, "vfkit"))
	port := freeLoopbackPort(t)
	writeAutoForwardReport(t, st, vm, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now(), Listeners: []GuestListener{
		{Proto: "tcp", Addr: "*", Port: port, PID: 42, Process: "Python"},
		{Proto: "tcp", Addr: "127.0.0.1", Port: 4321},
		{Proto: "tcp", Addr: "*", Port: 22},
	}})
	rows, err := manager.ReconcileAutoForwards(context.Background(), vm.Name)
	if err != nil {
		t.Fatal(err)
	}
	installed := 0
	for _, row := range rows {
		if row.Installed() {
			installed++
			if row.GuestPort != port || row.GuestTargetIP != "192.168.127.3" {
				t.Fatalf("wrong Darwin forward: %+v", row)
			}
		}
	}
	if installed != 1 {
		t.Fatalf("installed %d forwards: %+v", installed, rows)
	}
	writeAutoForwardReport(t, st, vm, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now(), Listeners: []GuestListener{}})
	if _, err := manager.ReconcileAutoForwards(context.Background(), vm.Name); err != nil {
		t.Fatal(err)
	}
	if exposes, removes := proxy.counts(); exposes != 1 || removes != 1 {
		t.Fatalf("forward reconciliation: adds=%d removes=%d", exposes, removes)
	}
}
