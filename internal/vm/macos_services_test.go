package vm

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/egress"
	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/host"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/share"
	"github.com/mjrusso/voom/internal/state"
)

func TestMacOSUnsupportedCommandsHaveNoSideEffects(t *testing.T) {
	st, record, _ := macosResetFixture(t)
	manager := New(st)
	ctx := context.Background()
	enabled := true
	offset := 10000
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"share", func() error {
			_, err := manager.AddShare(ctx, record.Name, "src", t.TempDir(), "/Volumes/src", true)
			return err
		}},
		{"automatic forwarding", func() error {
			_, err := manager.UpdateAutoForward(ctx, record.Name, AutoForwardUpdate{Enabled: &enabled})
			return err
		}},
		{"offset", func() error {
			_, err := manager.UpdateAutoForward(ctx, record.Name, AutoForwardUpdate{Offset: &offset})
			return err
		}},
		{"listeners", func() error { _, err := manager.ReadGuestPorts(record); return err }},
		{"reconcile", func() error { _, err := manager.ReconcileAutoForwards(ctx, record.Name); return err }},
		{"watch loop", func() error { return manager.WatchAutoForwards(ctx, record.ID) }},
		{"egress", func() error {
			_, err := manager.SetEgress(ctx, record.Name, egress.Decl{}, EgressOptions{})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := st.LoadVM(record.Name)
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), "not supported for MacOS") {
				t.Fatalf("unsupported command: %v", err)
			}
			after, err := st.LoadVM(record.Name)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("declaration changed: %+v %v", after, err)
			}
			if _, err := os.Stat(st.RuntimeVMDir(record)); !os.IsNotExist(err) {
				t.Fatalf("runtime allocated: %v", err)
			}
		})
	}
	path := st.Runtime(record).ForwardMutation()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("pending mutation")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := manager.UpdateAutoForward(ctx, record.Name, AutoForwardUpdate{Enabled: &enabled})
	if err == nil || !strings.Contains(err.Error(), "not supported for MacOS") {
		t.Fatal(err)
	}
	_, err = manager.ReconcileAutoForwards(ctx, record.Name)
	if err == nil || !strings.Contains(err.Error(), "not supported for MacOS") {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("pending mutation changed: %q %v", got, err)
	}
}

func TestMacOSPersistedUnsupportedDeclarationsRejectBeforeRuntimeChanges(t *testing.T) {
	for _, feature := range []string{"share", "auto-forward", "egress", "disabled egress"} {
		t.Run(feature, func(t *testing.T) {
			st, record, image := macosResetFixture(t)
			switch feature {
			case "share":
				record.Shares = []share.Decl{{Tag: "src", HostPath: t.TempDir(), GuestPath: "/Volumes/src"}}
			case "auto-forward":
				record.Network.AutoForward = true
			default:
				record.Network.Egress = &egress.Decl{Enabled: feature == "egress"}
			}
			if err := st.SaveVM(record); err != nil {
				t.Fatal(err)
			}
			if _, err := st.RemoveImage(record.Image.Name, true); err != nil {
				t.Fatal(err)
			}
			driver := fakeNamedVMProcess(t, "vfkit")
			writeProcessRecord(t, st.Runtime(record).VMProcessRecord(), driver)
			manager := New(st)
			ctx := context.Background()
			for _, run := range []func() error{
				func() error { _, err := manager.Start(ctx, io.Discard, record.Name); return err },
				func() error { return manager.ResetDisk(ctx, record.Name, image.Name) },
			} {
				if err := run(); err == nil || !strings.Contains(err.Error(), "remove these declarations") {
					t.Fatalf("unsupported declaration accepted: %v", err)
				}
				if !manager.IsRunning(record) {
					t.Fatal("rejected operation stopped the VM")
				}
			}
			if err := process.StopRecorded(st.Runtime(record).VMProcessRecord(), "vfkit", processStopTimeout); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Stop(ctx, record.Name); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Clone(ctx, record.Name, "copy"); err == nil || !strings.Contains(err.Error(), "remove these declarations") {
				t.Fatalf("clone accepted: %v", err)
			}
			saved, err := st.LoadVM(record.Name)
			if err != nil || !reflect.DeepEqual(saved.Network, record.Network) {
				t.Fatalf("configuration changed: %+v %v", saved, err)
			}
			if err := manager.Remove(ctx, record.Name); err != nil {
				t.Fatalf("cleanup blocked: %v", err)
			}
		})
	}
}

func TestMacOSMinimalGuestPreparationAndVFKitCapabilities(t *testing.T) {
	st, record, image := macosResetFixture(t)
	manager := New(st)
	if err := manager.prepareGuestFiles(record, image); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{st.SeedImagePath(record), st.ControlShareDir(record)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unsupported guest service file %s: %v", path, err)
		}
	}
	vfkit := filepath.Join(t.TempDir(), "vfkit")
	if err := os.WriteFile(vfkit, []byte("#!/bin/sh\necho '{\"gui-lifecycle-v1\":1,\"gui-branding-v1\":1}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VOOM_VFKIT", vfkit)
	if _, err := requireDriver(context.Background(), record); err != nil {
		t.Fatalf("minimal MacOS requires sharing extension: %v", err)
	}

	linux := *record
	linux.MacOS, linux.Arch = nil, "aarch64-linux"
	linux.Shares = []share.Decl{{Readonly: true}}
	if _, err := requireDriver(context.Background(), &linux); err == nil || !strings.Contains(err.Error(), "virtiofs-readonly-v1") {
		t.Fatalf("Linux sharing extension no longer required: %v", err)
	}
}

func TestMacOSInstallsSSHAndManualForwards(t *testing.T) {
	st, manager, record, proxy := newAutoForwardFixture(t)
	record.MacOS = &state.MacOSPlatform{IdentitySHA256: strings.Repeat("a", 64), CPUs: 4, MemoryMiB: 8192, MinimumCPUs: 4, MinimumMemoryMiB: 8192, Version: "27.0.1", Build: "26A434"}
	record.SchemaVersion, record.Arch, record.Driver = 2, "aarch64-darwin", "vfkit"
	record.StorageGeneration = strings.Repeat("a", 32)
	record.Resources = state.VMResources{CPUs: 4, MemoryMiB: 8192}
	record.Network = state.VMNetwork{SSHPort: 2222, SSHBind: "127.0.0.1"}
	record.Network.Forwards = []forward.Decl{{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 18000, GuestPort: 8000}}
	if err := st.SaveVM(record); err != nil {
		t.Fatal(err)
	}
	if err := manager.installDeclaredForwardsLocked(record); err != nil {
		t.Fatal(err)
	}
	for local, want := range map[string]string{
		"127.0.0.1:2222":  "192.168.127.3:22",
		"127.0.0.1:18000": "192.168.127.3:8000",
	} {
		if remote, ok := proxy.remote(local); !ok || remote != want {
			t.Fatalf("mapping %s: got %q, %v; want %s", local, remote, ok, want)
		}
	}
}

func TestMacOSCapabilitiesRejectedBeforeCreateAndStart(t *testing.T) {
	st, record, image := macosResetFixture(t)
	image.Capabilities.ControlShare = true
	if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(image.ID), "image.json"), image); err != nil {
		t.Fatal(err)
	}
	manager := New(st)
	ctx := context.Background()
	if _, err := manager.Create(ctx, "new", image.Name, "vfkit", 0, 0, 0); err == nil || !strings.Contains(err.Error(), "re-bake") {
		t.Fatalf("create accepted unsupported capabilities: %v", err)
	}
	record.Image = state.VMImageRef{ID: image.ID, Name: image.Name}
	if err := st.SaveVM(record); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(ctx, io.Discard, record.Name); err == nil || !strings.Contains(err.Error(), "re-bake") {
		t.Fatalf("start accepted unsupported capabilities: %v", err)
	}
	if _, err := os.Stat(st.RuntimeVMDir(record)); !os.IsNotExist(err) {
		t.Fatalf("runtime allocated: %v", err)
	}
}

func TestCreateMinimalMacOSGuest(t *testing.T) {
	if err := host.ValidateHostImage("aarch64-darwin", "raw", "vfkit"); err != nil {
		t.Skip(err)
	}
	st, _, image := macosResetFixture(t)
	record, err := New(st).Create(context.Background(), "minimal", image.Name, "vfkit", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if record.Network.AutoForward || len(record.Network.Forwards) != 0 || len(record.Shares) != 0 || record.Network.SSHPort == 0 {
		t.Fatalf("unexpected configuration: %+v", record)
	}
	if err := record.MacOS.ValidateStorage(st.VMStorageDir(record)); err != nil {
		t.Fatal(err)
	}
}

func TestMacOSUnsupportedDeclarationsCanBeRemoved(t *testing.T) {
	st, record, image := macosResetFixture(t)
	image.Capabilities = state.ImageCapabilities{ControlShare: true, GuestShareMount: true, GuestPortReport: true}
	if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(image.ID), "image.json"), image); err != nil {
		t.Fatal(err)
	}
	record.Image = state.VMImageRef{ID: image.ID, Name: image.Name}
	record.Shares = []share.Decl{{Tag: "src", HostPath: t.TempDir(), GuestPath: "/Volumes/src"}}
	record.Network.Forwards = []forward.Decl{{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 18080, GuestPort: 8080}}
	record.Network.AutoForward = true
	record.Network.Egress = &egress.Decl{}
	if err := st.SaveVM(record); err != nil {
		t.Fatal(err)
	}
	manager := New(st)
	ctx := context.Background()
	if _, err := manager.ForwardRows(ctx, []string{record.Name}); err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveShare(ctx, record.Name, "src"); err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveForward(ctx, record.Name, 18080, "127.0.0.1", "tcp"); err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err := manager.UpdateAutoForward(ctx, record.Name, AutoForwardUpdate{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ClearEgress(ctx, record.Name, EgressOptions{}); err != nil {
		t.Fatal(err)
	}
	saved, err := st.LoadVM(record.Name)
	if err != nil || len(saved.Shares) != 0 || len(saved.Network.Forwards) != 0 || saved.Network.AutoForward || saved.Network.Egress != nil {
		t.Fatalf("cleanup failed: %+v %v", saved, err)
	}
	if err := manager.Remove(ctx, record.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RemoveImage(image.Name, true); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxVFKitRetainsGuestServices(t *testing.T) {
	st, manager, record, _ := newAutoForwardFixture(t)
	record.Arch, record.Driver = "aarch64-linux", "vfkit"
	if err := st.SaveVM(record); err != nil {
		t.Fatal(err)
	}
	image, err := st.LoadImageByID(record.Image.ID)
	if err != nil {
		t.Fatal(err)
	}
	image.Capabilities.GuestShareMount = true
	image.Metadata.KernelPath = "/kernel"
	if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(image.ID), "image.json"), image); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := manager.AddShare(ctx, record.Name, "src", t.TempDir(), "/mnt/src", true); err != nil {
		t.Fatal(err)
	}
	record, err = st.LoadVM(record.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.WriteControlFiles(record); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.ControlShareMountsPath(record)); err != nil {
		t.Fatal(err)
	}
	control, err := manager.StartControlShare(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	shares, err := manager.StartUserShares(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	args, err := manager.vfkitArgs(record, image, append([]share.Runtime{control}, shares...))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mountTag=voom-control", "mountTag=src,readonly"} {
		if !strings.Contains(strings.Join(args, " "), want) {
			t.Fatalf("missing Linux share %s: %v", want, args)
		}
	}
	writeProcessRecord(t, st.Runtime(record).VMProcessRecord(), fakeNamedVMProcess(t, "vfkit"))
	port := freeLoopbackPort(t)
	writeAutoForwardReport(t, st, record, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now(), Listeners: []GuestListener{{Proto: "tcp", Addr: "*", Port: port}}})
	rows, err := manager.ReconcileAutoForwards(ctx, record.Name)
	if err != nil || len(rows) != 1 || !rows[0].Installed() {
		t.Fatalf("Linux auto-forwarding: %+v %v", rows, err)
	}
}
