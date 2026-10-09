package vm

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/mjrusso/voom/internal/pci"
	"github.com/mjrusso/voom/internal/state"
)

func TestPCIAssignmentsRequireStoppedVM(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("PCI passthrough is Linux-only")
	}
	st, _ := newTestStore(t)
	vmRec := &state.VMRecord{SchemaVersion: state.SchemaVersion, ID: "vm1", Name: "one", Driver: "qemu", Arch: "x86_64-linux", PCIDevices: []pci.Decl{missingGPU()}}
	if err := os.MkdirAll(st.Runtime(vmRec).Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVM(vmRec); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(vmRec.Name, vmRec.ID); err != nil {
		t.Fatal(err)
	}
	writeProcessRecord(t, st.Runtime(vmRec).VMProcessRecord(), fakeNamedVMProcess(t, "qemu-system-test"))
	mgr := New(st)
	if _, err := mgr.AddPCIDevice(context.Background(), "one", "nic", "0000:fe:1e"); err == nil || !strings.Contains(err.Error(), "is running") {
		t.Fatalf("AddPCIDevice error = %v", err)
	}
	if err := mgr.RemovePCIDevice(context.Background(), "one", "gpu"); err == nil || !strings.Contains(err.Error(), "is running") {
		t.Fatalf("RemovePCIDevice error = %v", err)
	}
}

func TestPreparePCIDevicesNamesTheFailingAssignment(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("PCI passthrough is Linux-only")
	}
	st, _ := newTestStore(t)
	vmRec := &state.VMRecord{ID: "vm1", Name: "one", Driver: "qemu", Arch: "x86_64-linux", PCIDevices: []pci.Decl{missingGPU()}}
	err := New(st).preparePCIDevices(vmRec)
	if err == nil || !strings.Contains(err.Error(), `PCI device "gpu": no PCI device at 0000:fe:1f`) {
		t.Fatalf("preparePCIDevices error = %v", err)
	}
}

func TestMemlockError(t *testing.T) {
	const gib = 1 << 30
	for _, tc := range []struct {
		limit uint64
		ok    bool
	}{
		{limit: unix.RLIM_INFINITY, ok: true},
		{limit: 9 * gib, ok: true},
		{limit: 9*gib - 1},
		{limit: 8 << 20},
	} {
		err := memlockError(tc.limit, 8192)
		if tc.ok != (err == nil) {
			t.Errorf("memlockError(%d, 8192) = %v", tc.limit, err)
		}
	}
}

// missingGPU is assigned at a slot that does not exist on the test host.
func missingGPU() pci.Decl {
	return pci.Decl{Name: "gpu", Slot: pci.Slot{Bus: 0xfe, Device: 0x1f}, Functions: []pci.Function{{Number: 0, Vendor: "10de", Device: "1e84"}}}
}
