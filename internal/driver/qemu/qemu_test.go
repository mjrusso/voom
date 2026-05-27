package qemu

import "testing"

func TestArgsIncludesVirtiofsDevices(t *testing.T) {
	args := Args(ArgsConfig{
		CPUs:        2,
		MemoryMiB:   512,
		DiskPath:    "/state/vm/disk.qcow2",
		DiskFormat:  "qcow2",
		SeedImage:   "/run/vm/seed.img",
		NetSock:     "/run/vm/qemu-net.sock",
		MAC:         "02:00:00:00:00:01",
		SerialLog:   "/logs/serial.log",
		MonitorSock: "/run/vm/qemu.mon",
		Pidfile:     "/run/vm/vm.pid",
		Shares:      []Share{{Tag: "voom-control", Sock: "/run/vm/control.sock"}, {Tag: "src", Sock: "/run/vm/src.sock"}},
	})
	for _, pair := range [][2]string{
		{"-object", "memory-backend-memfd,id=mem,size=512M,share=on"},
		{"-numa", "node,memdev=mem"},
		{"-drive", "file=/state/vm/disk.qcow2,if=virtio,format=qcow2"},
		{"-drive", "file=/run/vm/seed.img,if=virtio,format=raw,readonly=on"},
		{"-netdev", "stream,id=net0,server=off,addr.type=unix,addr.path=/run/vm/qemu-net.sock"},
		{"-device", "virtio-net-pci,netdev=net0,mac=02:00:00:00:00:01"},
		{"-chardev", "socket,id=chrfs0,path=/run/vm/control.sock"},
		{"-device", "vhost-user-fs-pci,chardev=chrfs1,tag=src,queue-size=1024"},
	} {
		if !hasArgPair(args, pair[0], pair[1]) {
			t.Fatalf("missing %s %s in %#v", pair[0], pair[1], args)
		}
	}
}

func TestArgsOmitsSharedMemoryWithoutVirtiofs(t *testing.T) {
	args := Args(ArgsConfig{CPUs: 1, MemoryMiB: 256})
	if hasArg(args, "-object") || hasArg(args, "-numa") {
		t.Fatalf("unexpected shared memory args without virtiofs: %#v", args)
	}
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
