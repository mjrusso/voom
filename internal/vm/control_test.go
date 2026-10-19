package vm

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/control"
	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/share"
	"github.com/mjrusso/voom/internal/state"
)

func TestControlReconnectReplaysAndClearsReports(t *testing.T) {
	st, vm, im := macosResetFixture(t)
	im.Capabilities = state.ImageCapabilities{VsockControl: true, GuestShareMount: true, GuestPortReport: true}
	manager := New(st)
	vm.Shares = []share.Decl{{Tag: "src", GuestPath: "/Volumes/src", Readonly: true}}
	if err := manager.WriteControlFiles(vm, true); err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		host, guest := net.Pipe()
		done := make(chan error, 1)
		go func() { done <- manager.controlSession(vm, host); _ = host.Close() }()
		if err := control.Write(guest, "hello", nil); err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReaderSize(guest, control.MaxMessage)
		raw, err := control.Read(reader, "config")
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			Mounts []share.Decl `json:"mounts"`
		}
		if err := json.Unmarshal(raw, &config); err != nil || len(config.Mounts) != 1 || !config.Mounts[0].Readonly {
			t.Fatalf("configuration replay %d: %s %v", attempt, raw, err)
		}
		report := forward.PortsReport{SchemaVersion: 1, GeneratedAt: time.Now(), Listeners: []forward.Listener{}}
		if err := control.Write(guest, "report", map[string]any{"ports": report, "errors": []string{}}); err != nil {
			t.Fatal(err)
		}
		if _, err := control.Read(reader, "config"); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.ReadGuestPorts(vm); err != nil {
			t.Fatal(err)
		}
		_ = guest.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("session did not close")
		}
		if _, err := os.Stat(st.ControlSharePortsPath(vm)); !os.IsNotExist(err) {
			t.Fatalf("disconnected report remains: %v", err)
		}
	}
	shares, err := manager.StartUserShares(t.Context(), vm)
	if err != nil {
		t.Fatal(err)
	}
	args, err := manager.vfkitArgs(vm, im, shares)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, arg := range args {
		if arg == "virtio-vsock,port=49152,socketURL="+st.Runtime(vm).ControlSock()+",listen" {
			found = true
		}
		if arg == "virtio-fs,sharedDir="+st.ControlShareDir(vm)+",mountTag=voom-control" {
			t.Fatal("reserved share attached")
		}
	}
	if !found {
		t.Fatal("missing vfkit vsock device")
	}
}

func TestVsockReportLossRemovesInstalledForwards(t *testing.T) {
	st, manager, vm, fake := newAutoForwardFixture(t)
	image, err := st.LoadImageByID(vm.Image.ID)
	if err != nil {
		t.Fatal(err)
	}
	image.Capabilities.ControlShare = false
	image.Capabilities.VsockControl = true
	if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(image.ID), "image.json"), image); err != nil {
		t.Fatal(err)
	}
	writeAutoForwardReport(t, st, vm, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now(), Listeners: []forward.Listener{{Proto: "tcp", Addr: "0.0.0.0", Port: 18432}}})
	if _, err := manager.reconcileAutoForwardsLocked(vm); err != nil {
		t.Fatal(err)
	}
	if len(fake.installed) != 1 {
		t.Fatalf("installed: %v", fake.installed)
	}
	if err := os.Remove(st.ControlSharePortsPath(vm)); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.reconcileAutoForwardsLocked(vm); err == nil {
		t.Fatal("missing report accepted")
	}
	if len(fake.installed) != 0 {
		t.Fatalf("disconnected guest retained forwards: %v", fake.installed)
	}
}

func TestControlRejectsIncompleteListenerReport(t *testing.T) {
	st, vm, _ := macosResetFixture(t)
	manager := New(st)
	if err := manager.WriteControlFiles(vm, true); err != nil {
		t.Fatal(err)
	}
	host, guest := net.Pipe()
	defer func() { _ = guest.Close() }()
	done := make(chan error, 1)
	go func() { done <- manager.controlSession(vm, host); _ = host.Close() }()
	if err := control.Write(guest, "hello", nil); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReaderSize(guest, control.MaxMessage)
	if _, err := control.Read(reader, "config"); err != nil {
		t.Fatal(err)
	}
	if err := control.Write(guest, "report", map[string]any{"ports": map[string]any{"schemaVersion": 1, "listeners": []any{}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("invalid report accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("invalid report did not close the session")
	}
	if _, err := os.Stat(st.ControlSharePortsPath(vm)); !os.IsNotExist(err) {
		t.Fatalf("invalid report was cached: %v", err)
	}
}

func TestMacOSResetRejectsLegacyControlBeforeStopping(t *testing.T) {
	st, vm, image := macosResetFixture(t)
	image.Capabilities = state.ImageCapabilities{ControlShare: true, GuestPortReport: true}
	if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(image.ID), "image.json"), image); err != nil {
		t.Fatal(err)
	}
	driver := fakeNamedVMProcess(t, "vfkit")
	writeProcessRecord(t, st.Runtime(vm).VMProcessRecord(), driver)
	if err := New(st).ResetDisk(t.Context(), vm.Name, image.Name); err == nil {
		t.Fatal("legacy MacOS reset accepted")
	}
	if pid, running := process.ValidRecord(st.Runtime(vm).VMProcessRecord(), "vfkit"); !running || pid != driver.Process.Pid {
		t.Fatal("rejected reset stopped driver")
	}
}
