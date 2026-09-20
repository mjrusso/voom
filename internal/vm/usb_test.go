package vm

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/mjrusso/voom/internal/state"
	"github.com/mjrusso/voom/internal/usb"
)

func TestAddUSBDeviceAllowsLocationAssignedToStoppedVMs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("USB passthrough is Linux-only")
	}
	st, _ := newTestStore(t)
	for _, vmRec := range []*state.VMRecord{
		{SchemaVersion: state.SchemaVersion, ID: "vm1", Name: "one", Driver: "qemu"},
		{SchemaVersion: state.SchemaVersion, ID: "vm2", Name: "two", Driver: "qemu"},
	} {
		if err := os.MkdirAll(st.VMDir(vmRec.ID), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveVM(vmRec); err != nil {
			t.Fatal(err)
		}
		if err := st.RegisterVM(vmRec.Name, vmRec.ID); err != nil {
			t.Fatal(err)
		}
	}

	mgr := New(st)
	mgr.scanUSB = scanTestUSB
	if _, err := mgr.AddUSBDevice(context.Background(), "one", "board", "usb-test-controller@2-255"); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.AddUSBDevice(context.Background(), "two", "probe", "usb-test-controller@2-255"); err != nil {
		t.Fatal(err)
	}
}

func TestAddUSBDeviceChecksConflictOnlyForRunningTarget(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("USB passthrough is Linux-only")
	}
	for _, targetRunning := range []bool{false, true} {
		name := "stopped"
		if targetRunning {
			name = "running"
		}
		t.Run(name, func(t *testing.T) {
			device := usb.Decl{Name: "board", Route: usb.Route{Controller: "test-controller", Protocol: 2, Port: "255"}}
			st, _ := newTestStore(t)
			vms := []*state.VMRecord{
				{SchemaVersion: state.SchemaVersion, ID: "vm1", Name: "one", Driver: "qemu", USBDevices: []usb.Decl{device}},
				{SchemaVersion: state.SchemaVersion, ID: "vm2", Name: "two", Driver: "qemu"},
			}
			for i, vmRec := range vms {
				if err := os.MkdirAll(st.VMDir(vmRec.ID), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := st.SaveVM(vmRec); err != nil {
					t.Fatal(err)
				}
				if err := st.RegisterVM(vmRec.Name, vmRec.ID); err != nil {
					t.Fatal(err)
				}
				if i == 0 || targetRunning {
					if err := os.MkdirAll(st.Runtime(vmRec).Dir(), 0o755); err != nil {
						t.Fatal(err)
					}
					writeProcessRecord(t, st.Runtime(vmRec).VMProcessRecord(), fakeNamedVMProcess(t, "qemu-system-test"))
				}
			}

			mgr := New(st)
			mgr.scanUSB = scanTestUSB
			_, err := mgr.AddUSBDevice(context.Background(), "two", "probe", device.Location())
			if targetRunning {
				if err == nil || !strings.Contains(err.Error(), "active or unconfirmed runtime") {
					t.Fatalf("running target conflict error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			loaded, err := st.LoadVM("two")
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if targetRunning {
				want = 0
			}
			if len(loaded.USBDevices) != want {
				t.Fatalf("target USB devices = %#v, want %d", loaded.USBDevices, want)
			}
		})
	}
}

func TestPrepareUSBDevicesUsesOneInventory(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("USB passthrough is Linux-only")
	}
	st, _ := newTestStore(t)
	mgr := New(st)
	scans := 0
	mgr.scanUSB = func() (usbInventory, error) {
		scans++
		return testUSBInventory{}, nil
	}
	vmRec := &state.VMRecord{
		Driver: "qemu",
		USBDevices: []usb.Decl{
			{Name: "board", Route: usb.Route{Controller: "controller", Protocol: 2, Port: "1"}},
			{Name: "probe", Route: usb.Route{Controller: "controller", Protocol: 2, Port: "2"}},
		},
	}

	bindings, err := mgr.prepareUSBDevices(vmRec)
	if err != nil {
		t.Fatal(err)
	}
	if scans != 1 || len(bindings) != 2 {
		t.Fatalf("inventory scans = %d, bindings = %#v", scans, bindings)
	}
}

func TestAddUSBDeviceRejectsUnavailableController(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("USB passthrough is Linux-only")
	}
	st, _ := newTestStore(t)
	vmRec := &state.VMRecord{SchemaVersion: state.SchemaVersion, ID: "vm1", Name: "dev", Driver: "qemu"}
	if err := os.MkdirAll(st.VMDir(vmRec.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVM(vmRec); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(vmRec.Name, vmRec.ID); err != nil {
		t.Fatal(err)
	}

	mgr := New(st)
	mgr.scanUSB = func() (usbInventory, error) {
		return testUSBInventory{resolveErr: errors.New("USB controller route is not available")}, nil
	}
	if _, err := mgr.AddUSBDevice(context.Background(), "dev", "board", "usb-missing@2-1"); err == nil || !strings.Contains(err.Error(), "controller route") {
		t.Fatalf("AddUSBDevice unavailable controller error = %v", err)
	}
	loaded, err := st.LoadVM("dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.USBDevices) != 0 {
		t.Fatalf("USB assignment persisted after rejected add: %#v", loaded.USBDevices)
	}
}
