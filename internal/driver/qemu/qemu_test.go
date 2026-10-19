package qemu

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mjrusso/voom/internal/host"
	"github.com/mjrusso/voom/internal/pci"
	"github.com/mjrusso/voom/internal/usb"
)

func TestRequireUSB(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "qemu-system-test")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf '%s\\n' 'name \"qemu-xhci\"' 'name \"usb-host\"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(host.ExeOverrideName(ExecutableName("test-linux")), exe)
	if err := RequireUSB("test-linux"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf '%s\\n' 'name \"qemu-xhci\"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RequireUSB("test-linux"); err == nil || !strings.Contains(err.Error(), "usb-host") {
		t.Fatalf("RequireUSB error = %v", err)
	}
}

func TestArgsIncludesVirtiofsDevices(t *testing.T) {
	args := mustArgs(t, ArgsConfig{
		Arch:        "x86_64-linux",
		CPUs:        2,
		MemoryMiB:   512,
		DiskPath:    "/state/vm/disk.qcow2",
		DiskFormat:  "qcow2",
		SeedImage:   "/run/vm/seed.img",
		NetSock:     "/run/vm/qemu-net.sock",
		MAC:         "02:00:00:00:00:01",
		SerialLog:   "/logs/serial.log",
		MonitorSock: "/run/vm/qemu.mon",
		Shares:      []Share{{Tag: "voom-control", Sock: "/run/vm/control.sock"}, {Tag: "src", Sock: "/run/vm/src.sock"}},
	})
	for _, pair := range [][2]string{
		{"-object", "memory-backend-memfd,id=mem,size=512M,share=on"},
		{"-numa", "node,memdev=mem"},
		{"-drive", "file=/state/vm/disk.qcow2,if=virtio,format=qcow2"},
		{"-drive", "file=/run/vm/seed.img,if=virtio,format=raw,readonly=on"},
		{"-netdev", "stream,id=net0,server=off,addr.type=unix,addr.path=/run/vm/qemu-net.sock"},
		{"-device", "virtio-net-pci,netdev=net0,mac=02:00:00:00:00:01"},
		{"-qmp", "unix:/run/vm/qemu.mon,server=on,wait=off"},
		{"-chardev", "socket,id=chrfs0,path=/run/vm/control.sock"},
		{"-device", "vhost-user-fs-pci,chardev=chrfs1,tag=src,queue-size=1024"},
	} {
		if !hasArgPair(args, pair[0], pair[1]) {
			t.Fatalf("missing %s %s in %#v", pair[0], pair[1], args)
		}
	}
	if hasArg(args, "-pidfile") {
		t.Fatalf("unexpected pidfile argument in %#v", args)
	}
	if hasArg(args, "-monitor") {
		t.Fatalf("unexpected HMP monitor argument in %#v", args)
	}
}

func TestArgsSelectsMachineByArch(t *testing.T) {
	x86 := mustArgs(t, ArgsConfig{Arch: "x86_64-linux", CPUs: 1, MemoryMiB: 256})
	if !hasArgPair(x86, "-machine", "q35") || hasArg(x86, "-bios") {
		t.Fatalf("x86_64 machine args = %#v", x86)
	}
	arm := mustArgs(t, ArgsConfig{Arch: "aarch64-linux", FirmwarePath: "/fw/QEMU_EFI.fd", CPUs: 1, MemoryMiB: 256})
	if !hasArgPair(arm, "-machine", "virt") || !hasArgPair(arm, "-bios", "/fw/QEMU_EFI.fd") {
		t.Fatalf("aarch64 machine args = %#v", arm)
	}
}

func TestArgsOmitsSharedMemoryWithoutVirtiofs(t *testing.T) {
	args := mustArgs(t, ArgsConfig{Arch: "x86_64-linux", CPUs: 1, MemoryMiB: 256})
	if hasArg(args, "-object") || hasArg(args, "-numa") {
		t.Fatalf("unexpected shared memory args without virtiofs: %#v", args)
	}
}

func TestArgsIncludesUSBPassthrough(t *testing.T) {
	args := mustArgs(t, ArgsConfig{Arch: "x86_64-linux", CPUs: 1, MemoryMiB: 256, USBDevices: []usb.Binding{{Name: "board", Bus: 1, Port: "3.2"}, {Name: "probe", Bus: 2, Port: "4"}}})
	for _, pair := range [][2]string{
		{"-device", "qemu-xhci,id=voom-xhci"},
		{"-device", "usb-host,bus=voom-xhci.0,hostbus=1,hostport=3.2,id=voom-usb-board"},
		{"-device", "usb-host,bus=voom-xhci.0,hostbus=2,hostport=4,id=voom-usb-probe"},
	} {
		if !hasArgPair(args, pair[0], pair[1]) {
			t.Fatalf("missing %s %s in %#v", pair[0], pair[1], args)
		}
	}
}

func TestArgsPlacesPCIDevicesBehindRootPorts(t *testing.T) {
	gpu := pci.Decl{Name: "gpu", Slot: pci.Slot{Bus: 0x41}, Functions: []pci.Function{
		{Number: 0, Vendor: "10de", Device: "1e84"},
		{Number: 1, Vendor: "10de", Device: "10f8"},
	}}
	audio := pci.Decl{Name: "audio", Slot: pci.Slot{Bus: 0x42}, Functions: []pci.Function{{Number: 1, Vendor: "10de", Device: "10f8"}}}
	// Enough automatically addressed devices to reach guest slot 0x10.
	shares := make([]Share, 16)
	for i := range shares {
		shares[i] = Share{Tag: fmt.Sprintf("share%d", i), Sock: fmt.Sprintf("/run/vm/share%d.sock", i)}
	}
	args := mustArgs(t, ArgsConfig{
		Arch:       "x86_64-linux",
		CPUs:       1,
		MemoryMiB:  256,
		DiskPath:   "/state/vm/disk.qcow2",
		SeedImage:  "/run/vm/seed.img",
		Shares:     shares,
		USBDevices: []usb.Binding{{Name: "board", Bus: 1, Port: "3.2"}},
		PCIDevices: []pci.Decl{gpu, audio},
	})
	for _, device := range []string{
		"pcie-root-port,id=voom-rp-gpu,bus=pcie.0,addr=10.0,chassis=1,slot=1,multifunction=on",
		"vfio-pci,host=0000:41:00.0,bus=voom-rp-gpu,addr=00.0,id=voom-pci-gpu-0,multifunction=on",
		"vfio-pci,host=0000:41:00.1,bus=voom-rp-gpu,addr=00.1,id=voom-pci-gpu-1",
		"pcie-root-port,id=voom-rp-audio,bus=pcie.0,addr=10.1,chassis=1,slot=2",
		"vfio-pci,host=0000:42:00.1,bus=voom-rp-audio,addr=00.0,id=voom-pci-audio-1",
	} {
		if !hasArgPair(args, "-device", device) {
			t.Fatalf("missing -device %s in %#v", device, args)
		}
	}
	lastRootPort := -1
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-device" && strings.HasPrefix(args[i+1], "pcie-root-port,") {
			lastRootPort = i
		}
	}
	for i := 0; i < lastRootPort; i++ {
		automatic := args[i] == "-drive" || args[i] == "-device" && !strings.HasPrefix(args[i+1], "pcie-root-port,")
		if automatic {
			t.Fatalf("%s %s precedes a root port in %#v", args[i], args[i+1], args)
		}
	}
}

func TestArgsOmitsUSBControllerWithoutDevices(t *testing.T) {
	args := mustArgs(t, ArgsConfig{Arch: "x86_64-linux", CPUs: 1, MemoryMiB: 256})
	if hasArgPair(args, "-device", "qemu-xhci,id=voom-xhci") {
		t.Fatalf("unexpected USB controller in %#v", args)
	}
}

func TestArgsRejectsInvalidUSBDeclaration(t *testing.T) {
	if _, err := Args(ArgsConfig{Arch: "x86_64-linux", CPUs: 1, MemoryMiB: 256, USBDevices: []usb.Binding{{Name: "bad,name", Bus: 1, Port: "2"}}}); err == nil {
		t.Fatal("Args accepted an invalid USB declaration")
	}
}

func mustArgs(t *testing.T, config ArgsConfig) []string {
	t.Helper()
	args, err := Args(config)
	if err != nil {
		t.Fatal(err)
	}
	return args
}

func hasArg(args []string, key string) bool {
	for _, arg := range args {
		if arg == key {
			return true
		}
	}
	return false
}

func hasArgPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestVsockUsesReservedDescriptorAndFirmwarePort(t *testing.T) {
	args, err := Args(ArgsConfig{Arch: "x86_64-linux", CPUs: 2, MemoryMiB: 512, ControlCID: 12345, ControlPort: 49153})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, expected := range []string{"vhost-vsock-pci,guest-cid=12345,vhostfd=3", "name=opt/voom/control-port,string=49153"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing %q from %s", expected, joined)
		}
	}
	for _, cid := range []uint32{1, 2, ^uint32(0)} {
		if _, err := Args(ArgsConfig{Arch: "x86_64-linux", ControlCID: cid, ControlPort: 49153}); err == nil {
			t.Fatalf("accepted reserved CID %d", cid)
		}
	}
}
