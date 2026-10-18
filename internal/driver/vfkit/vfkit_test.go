package vfkit

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestArgs(t *testing.T) {
	args := mustArgs(t, ArgsConfig{
		CPUs:      2,
		MemoryMiB: 2048,
		Linux: &LinuxBoot{
			KernelPath: "/nix/store/kernel/Image",
			InitrdPath: "/nix/store/initrd/initrd",
			KernelCmd:  "console=hvc0 root=/dev/vda1 init=/nix/store/system/init",
			SeedImage:  "/vm/seed.img", SerialLog: "/vm/serial.log",
		},
		BootDisk: "/vm/disk.raw",
		NetSock:  "/vm/vfkit.sock",
		MAC:      "02:12:34:56:78:9a",
		RestSock: "/vm/rest.sock",
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
		"virtio-fs,sharedDir=/src,mountTag=src,readonly",
		"--restful-uri", "unix:///vm/rest.sock",
	} {
		if !contains(args, want) {
			t.Fatalf("args missing %q: %#v", want, args)
		}
	}
	if slices.Index(args, "virtio-blk,path=/vm/disk.raw") > slices.Index(args, "virtio-blk,path=/vm/seed.img,readonly") {
		t.Fatal("seed disk precedes the guest's /dev/vda disk")
	}
	if contains(args, "--bootloader") {
		t.Fatalf("direct boot args unexpectedly include bootloader: %#v", args)
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
	args := mustArgs(t, ArgsConfig{
		CPUs:      1,
		MemoryMiB: 512,
		BootDisk:  "/vm/disk.img",
		NetSock:   "/vm/vfkit.sock",
		MAC:       "02:12:34:56:78:9a",
		RestSock:  "/vm/rest.sock",
		Linux:     &LinuxBoot{SerialLog: "/vm/serial.log", EFIStore: "/vm/efi.nvram"},
	})
	if !contains(args, "efi,variable-store=/vm/efi.nvram,create") {
		t.Fatalf("missing EFI bootloader: %v", args)
	}
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

func TestMacOSBootUsesPersistentPlatformAndOmitsLinuxDevices(t *testing.T) {
	args := mustArgs(t, ArgsConfig{
		CPUs: 4, MemoryMiB: 8192, BootDisk: "/vm/disk.raw", NetSock: "/runtime/net.sock", RestSock: "/runtime/rest.sock", MAC: "02:12:34:56:78:9a",
		MacOS: &MacOSBoot{MachineIdentifier: "/vm/MachineIdentifier", HardwareModel: "/vm/HardwareModel", AuxiliaryStorage: "/vm/AuxiliaryStorage", WindowTitle: "test-vm"},
	})
	if !contains(args, "macos,machineIdentifierPath=/vm/MachineIdentifier,hardwareModelPath=/vm/HardwareModel,auxImagePath=/vm/AuxiliaryStorage") {
		t.Fatalf("missing macOS boot platform: %v", args)
	}
	joined := strings.Join(args, " ")
	for _, forbidden := range []string{"seed.img", "virtio-serial", "efi,", "--kernel"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("macOS args include %s: %v", forbidden, args)
		}
	}
	if !contains(args, "--gui") || !contains(args, "virtio-net,unixSocketPath=/runtime/net.sock,mac=02:12:34:56:78:9a") || !contains(args, "virtio-gpu,width=1280,height=800") {
		t.Fatalf("missing macOS devices: %v", args)
	}
}

func TestDisplayControlReportsUnsupportedBackend(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "vfkit.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.NotFoundHandler()}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Close() }()
	if err := ShowDisplay(context.Background(), socket); err == nil || !strings.Contains(err.Error(), "restart the VM") {
		t.Fatalf("missing backend diagnostic: %v", err)
	}
}

func TestExtensionsRequireCompatibleExecutable(t *testing.T) {
	for _, tc := range []struct {
		output   string
		required []string
		valid    bool
	}{
		{`{"gui-lifecycle-v1":1}`, []string{"gui-lifecycle-v1"}, true},
		{`{}`, []string{"gui-lifecycle-v1"}, false},
		{`not JSON`, []string{"gui-lifecycle-v1"}, false},
		{`{"gui-lifecycle-v1":1}`, []string{"gui-lifecycle-v1", "virtiofs-readonly-v1"}, false},
		{`{"gui-lifecycle-v1":1,"virtiofs-readonly-v1":1}`, []string{"gui-lifecycle-v1", "virtiofs-readonly-v1"}, true},
	} {
		path := filepath.Join(t.TempDir(), "vfkit")
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '"+tc.output+"'\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("VOOM_VFKIT", path)
		selected, err := ResolveCompatible(context.Background(), tc.required...)
		if (err == nil) != tc.valid {
			t.Fatalf("capabilities %s required=%v: %v", tc.output, tc.required, err)
		}
		if tc.valid && selected != path {
			t.Fatalf("selected %q, want override %q", selected, path)
		}
		if !tc.valid && !strings.Contains(err.Error(), path) {
			t.Fatalf("diagnostic missing selected helper path: %v", err)
		}
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

func TestArgsRejectInvalidBootConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config ArgsConfig
	}{
		{"missing guest", ArgsConfig{}},
		{"conflicting guests", ArgsConfig{Linux: &LinuxBoot{}, MacOS: &MacOSBoot{}}},
		{"missing machine identifier", ArgsConfig{MacOS: &MacOSBoot{HardwareModel: "hardware", AuxiliaryStorage: "nvram"}}},
		{"missing hardware model", ArgsConfig{MacOS: &MacOSBoot{MachineIdentifier: "identity", AuxiliaryStorage: "nvram"}}},
		{"missing auxiliary storage", ArgsConfig{MacOS: &MacOSBoot{MachineIdentifier: "identity", HardwareModel: "hardware"}}},
		{"kernel and EFI", ArgsConfig{Linux: &LinuxBoot{KernelPath: "kernel", EFIStore: "efi"}}},
		{"missing EFI store", ArgsConfig{Linux: &LinuxBoot{}}},
		{"initrd without kernel", ArgsConfig{Linux: &LinuxBoot{EFIStore: "efi", InitrdPath: "initrd"}}},
		{"cmdline without kernel", ArgsConfig{Linux: &LinuxBoot{EFIStore: "efi", KernelCmd: "cmdline"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if args, err := Args(tc.config); err == nil || args != nil {
				t.Fatalf("invalid boot accepted: %v %v", args, err)
			}
		})
	}
}
