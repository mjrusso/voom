package vfkit

import (
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestArgs(t *testing.T) {
	args := Args(ArgsConfig{
		CPUs:       2,
		MemoryMiB:  2048,
		KernelPath: "/nix/store/kernel/Image",
		InitrdPath: "/nix/store/initrd/initrd",
		KernelCmd:  "console=hvc0 root=/dev/vda1 init=/nix/store/system/init",
		BootDisk:   "/vm/disk.raw",
		SeedImage:  "/vm/seed.img",
		NetSock:    "/vm/vfkit.sock",
		MAC:        "02:12:34:56:78:9a",
		SerialLog:  "/vm/serial.log",
		RestSock:   "/vm/rest.sock",
		EFIStore:   "/vm/efi.nvram",
		Shares: []Share{
			{MountTag: "voom-control", SharedDir: "/vm/control"},
			{MountTag: "src", SharedDir: "/src", Readonly: true},
		},
	})
	for _, want := range []string{
		"--kernel", "/nix/store/kernel/Image",
		"--initrd", "/nix/store/initrd/initrd",
		"--kernel-cmdline", "console=hvc0 root=/dev/vda1 init=/nix/store/system/init",
		"virtio-blk,path=/vm/disk.raw",
		"virtio-blk,path=/vm/seed.img,readonly",
		"virtio-net,unixSocketPath=/vm/vfkit.sock,mac=02:12:34:56:78:9a",
		"virtio-serial,logFilePath=/vm/serial.log",
		"virtio-fs,sharedDir=/vm/control,mountTag=voom-control",
		"virtio-fs,sharedDir=/src,mountTag=src",
		"--restful-uri", "unix:///vm/rest.sock",
	} {
		if !contains(args, want) {
			t.Fatalf("args missing %q: %#v", want, args)
		}
	}
	if contains(args, "--bootloader") {
		t.Fatalf("direct boot args unexpectedly include bootloader: %#v", args)
	}
	if contains(args, "virtio-fs,sharedDir=/src,mountTag=src,readonly") {
		t.Fatalf("vfkit virtio-fs args unexpectedly include unsupported readonly option: %#v", args)
	}
}

func TestStop(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "vfkit.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	seen := make(chan string, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen <- r.URL.Path + " " + string(b)
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	if err := Stop(sock, false); err != nil {
		t.Fatal(err)
	}
	got := <-seen
	if !strings.Contains(got, "/vm/state") || !strings.Contains(got, `"state":"Stop"`) {
		t.Fatalf("unexpected stop request: %s", got)
	}
}

func TestArgsOmitsSeedDiskWhenUnset(t *testing.T) {
	args := Args(ArgsConfig{
		CPUs:      1,
		MemoryMiB: 512,
		BootDisk:  "/vm/disk.img",
		NetSock:   "/vm/vfkit.sock",
		MAC:       "02:12:34:56:78:9a",
		SerialLog: "/vm/serial.log",
		RestSock:  "/vm/rest.sock",
		EFIStore:  "/vm/efi.nvram",
	})
	if contains(args, "virtio-blk,path=/vm/seed.img,readonly") {
		t.Fatalf("args unexpectedly include seed disk: %#v", args)
	}
}

func contains(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
