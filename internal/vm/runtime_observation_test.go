package vm

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/pci"
	"github.com/mjrusso/voom/internal/share"
	"github.com/mjrusso/voom/internal/state"
)

func TestStoppedOnlyOperationsRejectUnknownRuntime(t *testing.T) {
	operations := map[string]func(*Manager, string) error{
		"clone": func(m *Manager, _ string) error { _, err := m.Clone(context.Background(), "vm", "copy"); return err },
		"grow":  func(m *Manager, _ string) error { return m.GrowDisk(context.Background(), "vm", "1MiB") },
		"cpus":  func(m *Manager, _ string) error { _, _, err := m.SetCPUs(context.Background(), "vm", 4); return err },
		"memory": func(m *Manager, _ string) error {
			_, _, err := m.SetMemory(context.Background(), "vm", 4096)
			return err
		},
		"ssh": func(m *Manager, _ string) error {
			_, _, err := m.SetSSHPort(context.Background(), "vm", 2223)
			return err
		},
		"add share": func(m *Manager, dir string) error {
			_, err := m.AddShare(context.Background(), "vm", "new", dir, "/mnt/new", false)
			return err
		},
		"remove share": func(m *Manager, _ string) error { return m.RemoveShare(context.Background(), "vm", "old") },
		"add pci": func(m *Manager, _ string) error {
			_, err := m.AddPCIDevice(context.Background(), "vm", "nic", "0000:fe:1e")
			return err
		},
		"remove pci": func(m *Manager, _ string) error { return m.RemovePCIDevice(context.Background(), "vm", "gpu") },
		"forward": func(m *Manager, _ string) error {
			_, err := m.AddForward(context.Background(), "vm", 8080, 18080, "127.0.0.1", false, "tcp")
			return err
		},
		"automatic forward": func(m *Manager, _ string) error {
			disabled := false
			_, err := m.UpdateAutoForward(context.Background(), "vm", AutoForwardUpdate{Enabled: &disabled})
			return err
		},
	}
	for _, record := range []string{"malformed", "unreadable", "wrong executable", "missing record with driver socket"} {
		for name, operation := range operations {
			t.Run(record+"/"+name, func(t *testing.T) {
				if name == "add pci" && runtime.GOOS != "linux" {
					t.Skip("PCI passthrough is Linux-only")
				}
				st, dir := newTestStore(t, state.WithHostPortAvailable(func(string, int) (bool, string) { return true, "" }))
				im := &state.ImageRecord{SchemaVersion: 1, ID: "image", Name: "image", Arch: "x86_64-linux", Format: "raw", Disk: "disk.raw", Capabilities: state.ImageCapabilities{ControlShare: true, GuestShareMount: true}}
				if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(im.ID), "image.json"), im); err != nil {
					t.Fatal(err)
				}
				vm := &state.VMRecord{SchemaVersion: 1, ID: "vm", Name: "vm", Driver: "qemu", Arch: im.Arch, Image: state.VMImageRef{ID: im.ID, Name: im.Name}, Resources: state.VMResources{CPUs: 2, MemoryMiB: 2048}, Network: state.VMNetwork{SSHPort: 2222}, Shares: []share.Decl{{Tag: "old", HostPath: dir, GuestPath: "/mnt/old"}}, PCIDevices: []pci.Decl{missingGPU()}}
				if name == "grow" {
					vm.Driver = "vfkit"
					vm.PCIDevices = nil
				}
				if err := st.SaveVM(vm); err != nil {
					t.Fatal(err)
				}
				if err := st.RegisterVM(vm.Name, vm.ID); err != nil {
					t.Fatal(err)
				}
				disk := st.VMDiskPath(vm)
				if err := os.WriteFile(disk, []byte("live disk"), 0600); err != nil {
					t.Fatal(err)
				}
				rt := st.Runtime(vm)
				if err := os.MkdirAll(rt.Dir(), 0700); err != nil {
					t.Fatal(err)
				}
				switch record {
				case "malformed":
					if err := os.WriteFile(rt.VMProcessRecord(), []byte("invalid"), 0600); err != nil {
						t.Fatal(err)
					}
				case "unreadable":
					if err := os.Mkdir(rt.VMProcessRecord(), 0700); err != nil {
						t.Fatal(err)
					}
				case "wrong executable":
					writeProcessRecord(t, rt.VMProcessRecord(), fakeNamedVMProcess(t, "unrecognized-runtime"))
				case "missing record with driver socket":
					driver := "qemu-system-test"
					if vm.Driver == "vfkit" {
						driver = "vfkit"
					}
					writeProcessRecord(t, rt.VMProcessRecord(), fakeNamedVMProcess(t, driver))
					if err := os.Remove(rt.VMProcessRecord()); err != nil {
						t.Fatal(err)
					}
					listener, err := net.Listen("unix", rt.DriverArtifacts(vm.Driver)[0])
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = listener.Close() })
				}
				before, err := os.ReadFile(filepath.Join(st.VMDir(vm.ID), "vm.json"))
				if err != nil {
					t.Fatal(err)
				}
				err = operation(New(st), dir)
				if err == nil || !strings.Contains(err.Error(), "cannot confirm") {
					t.Errorf("operation accepted unknown runtime: %v", err)
				}
				after, err := os.ReadFile(filepath.Join(st.VMDir(vm.ID), "vm.json"))
				if err != nil || !bytes.Equal(before, after) {
					t.Errorf("VM record changed: %v", err)
				}
				data, err := os.ReadFile(disk)
				if err != nil || string(data) != "live disk" {
					t.Errorf("disk changed: length=%d, %v", len(data), err)
				}
				idx, err := state.ReadIndex(st.Paths().State)
				if err != nil || len(idx.VMs) != 1 {
					t.Errorf("index changed: %+v, %v", idx, err)
				}
			})
		}
	}
}

func TestObserveRuntimeDistinguishesStoppedRunningAndUnknown(t *testing.T) {
	st, _ := newTestStore(t)
	m := New(st)
	vm := &state.VMRecord{SchemaVersion: 1, ID: "vm", Name: "vm", Driver: "qemu", Resources: state.VMResources{CPUs: 2, MemoryMiB: 2048}}
	if err := st.SaveVM(vm); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(vm.Name, vm.ID); err != nil {
		t.Fatal(err)
	}
	check := func(want RuntimeState) {
		t.Helper()
		got, err := m.ObserveRuntime(vm)
		if got != want || (err != nil) != (want == RuntimeUnknown) {
			t.Fatalf("ObserveRuntime = %s, %v; want %s", got, err, want)
		}
	}
	check(RuntimeStopped)
	rt := st.Runtime(vm)
	if err := os.MkdirAll(rt.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", rt.DriverArtifacts(vm.Driver)[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	check(RuntimeUnknown)
	if err := os.Symlink(filepath.Join(rt.Dir(), "missing"), rt.VMProcessRecord()); err != nil {
		t.Fatal(err)
	}
	check(RuntimeUnknown)
	if err := os.Remove(rt.VMProcessRecord()); err != nil {
		t.Fatal(err)
	}
	driver := fakeNamedVMProcess(t, "qemu-system-test")
	writeProcessRecord(t, rt.VMProcessRecord(), driver)
	check(RuntimeRunning)
	if err := driver.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = driver.Process.Wait()
	check(RuntimeStopped)
	if _, changed, err := m.SetCPUs(context.Background(), vm.Name, 4); err != nil || !changed {
		t.Fatalf("confirmed exited process prevented update: changed=%t, %v", changed, err)
	}
}

func TestPreviousRuntimeArtifactsBlockMutations(t *testing.T) {
	st, _ := newTestStore(t)
	m := New(st)
	m.previousRuntime = filepath.Join(st.Paths().Runtime, "previous")
	endpoint := forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 8000, GuestPort: 80}
	vm := &state.VMRecord{
		SchemaVersion: 1, ID: "vm", Name: "vm", Driver: "vfkit",
		Resources: state.VMResources{CPUs: 2, MemoryMiB: 2048},
		Network:   state.VMNetwork{Forwards: []forward.Decl{endpoint}},
	}
	if err := st.SaveVM(vm); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(vm.Name, vm.ID); err != nil {
		t.Fatal(err)
	}
	disk := st.VMDiskPath(vm)
	if err := os.WriteFile(disk, []byte("guest disk"), 0600); err != nil {
		t.Fatal(err)
	}
	if status, err := m.ObserveRuntime(vm); status != RuntimeStopped || err != nil {
		t.Fatalf("without previous runtime: %s, %v", status, err)
	}

	old := state.RuntimeAt(m.previousRuntime, vm)
	if err := os.MkdirAll(old.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old.GVProxyProcessRecord(), []byte("legacy artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	checkBlocked := func() {
		t.Helper()
		hint := "VOOM_RUNTIME_DIR=" + m.previousRuntime + " voom stop " + vm.Name
		status, err := m.ObserveRuntime(vm)
		if status != RuntimeUnknown || err == nil || !strings.Contains(err.Error(), hint) {
			t.Fatalf("previous runtime: %s, %v", status, err)
		}
		if err := m.Remove(context.Background(), vm.Name); err == nil || !strings.Contains(err.Error(), hint) {
			t.Fatalf("Remove error: %v", err)
		}
		if _, err := m.Stop(context.Background(), vm.Name); err == nil || !strings.Contains(err.Error(), hint) {
			t.Fatalf("Stop error: %v", err)
		}
		if _, _, err := m.SetCPUs(context.Background(), vm.Name, 4); err == nil || !strings.Contains(err.Error(), hint) {
			t.Fatalf("SetCPUs error: %v", err)
		}
		data, err := os.ReadFile(disk)
		if err != nil || string(data) != "guest disk" {
			t.Fatalf("guest disk changed: %q, %v", data, err)
		}
	}
	checkBlocked()
	removeForward := func() error {
		return m.RemoveForward(context.Background(), vm.Name, endpoint.HostPort, endpoint.Bind, endpoint.Protocol)
	}
	for _, pending := range []bool{false, true} {
		journal := st.Runtime(vm).ForwardMutation()
		if pending {
			if err := state.WriteJSONAtomic(journal, []forward.Decl{endpoint}); err != nil {
				t.Fatal(err)
			}
		}
		if err := removeForward(); err == nil || !strings.Contains(err.Error(), old.GVProxyProcessRecord()) || !strings.Contains(err.Error(), "VOOM_RUNTIME_DIR=") {
			t.Fatalf("RemoveForward with pending recovery %t: %v", pending, err)
		}
		saved, err := st.LoadVM(vm.Name)
		if err != nil || len(saved.Network.Forwards) != 1 || saved.Network.Forwards[0] != endpoint {
			t.Fatalf("forward declaration changed: %v, %v", saved, err)
		}
		if pending {
			reservations, err := forward.ReadMutationReservations(journal)
			if err != nil || len(reservations) != 1 || reservations[0] != endpoint {
				t.Fatalf("pending recovery changed: %v, %v", reservations, err)
			}
			if err := os.Remove(journal); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Remove(old.GVProxyProcessRecord()); err != nil {
		t.Fatal(err)
	}
	if status, err := m.ObserveRuntime(vm); status != RuntimeStopped || err != nil {
		t.Fatalf("after previous runtime artifacts are cleared: %s, %v", status, err)
	}
	if err := removeForward(); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(context.Background(), vm.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(disk); !os.IsNotExist(err) {
		t.Fatalf("disk remains after removal: %v", err)
	}
}

func TestInvalidPreviousRuntimeOnlyBlocksVMsWithEvidence(t *testing.T) {
	st, _ := newTestStore(t)
	m := New(st)
	m.previousRuntime = filepath.Join(st.Paths().Runtime, "previous")
	vm := &state.VMRecord{SchemaVersion: 1, ID: "vm", Name: "vm", Driver: "vfkit"}
	old := state.RuntimeAt(m.previousRuntime, vm)
	if err := os.MkdirAll(old.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(m.previousRuntime, 0755); err != nil {
		t.Fatal(err)
	}
	if status, err := m.ObserveRuntime(vm); status != RuntimeStopped || err != nil {
		t.Fatalf("empty previous runtime: %s, %v", status, err)
	}
	if err := os.WriteFile(old.GVProxyProcessRecord(), []byte("unresolved"), 0600); err != nil {
		t.Fatal(err)
	}
	status, err := m.ObserveRuntime(vm)
	if status != RuntimeUnknown || err == nil || !strings.Contains(err.Error(), old.GVProxyProcessRecord()) {
		t.Fatalf("previous runtime with evidence: %s, %v", status, err)
	}
	other := &state.VMRecord{ID: "other", Name: "other", Driver: "vfkit"}
	if status, err := m.ObserveRuntime(other); status != RuntimeStopped || err != nil {
		t.Fatalf("unrelated VM: %s, %v", status, err)
	}
}
