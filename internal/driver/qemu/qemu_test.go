package qemu

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mjrusso/voom/internal/host"
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

func TestArgsOmitsSharedMemoryWithoutVirtiofs(t *testing.T) {
	args := mustArgs(t, ArgsConfig{CPUs: 1, MemoryMiB: 256})
	if hasArg(args, "-object") || hasArg(args, "-numa") {
		t.Fatalf("unexpected shared memory args without virtiofs: %#v", args)
	}
}

func TestArgsIncludesUSBPassthrough(t *testing.T) {
	args := mustArgs(t, ArgsConfig{CPUs: 1, MemoryMiB: 256, USBDevices: []usb.Binding{{Name: "board", Bus: 1, Port: "3.2"}, {Name: "probe", Bus: 2, Port: "4"}}})
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

func TestArgsOmitsUSBControllerWithoutDevices(t *testing.T) {
	args := mustArgs(t, ArgsConfig{CPUs: 1, MemoryMiB: 256})
	if hasArgPair(args, "-device", "qemu-xhci,id=voom-xhci") {
		t.Fatalf("unexpected USB controller in %#v", args)
	}
}

func TestArgsRejectsInvalidUSBDeclaration(t *testing.T) {
	if _, err := Args(ArgsConfig{CPUs: 1, MemoryMiB: 256, USBDevices: []usb.Binding{{Name: "bad,name", Bus: 1, Port: "2"}}}); err == nil {
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
