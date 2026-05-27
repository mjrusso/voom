package host

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolvePathsUsesOverrides(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("VOOM_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("VOOM_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("VOOM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("VOOM_RUNTIME_DIR", filepath.Join(dir, "runtime"))

	got := ResolvePaths()
	if got.Config != filepath.Join(dir, "config") ||
		got.State != filepath.Join(dir, "state") ||
		got.Cache != filepath.Join(dir, "cache") ||
		got.Runtime != filepath.Join(dir, "runtime") {
		t.Fatalf("unexpected paths: %#v", got)
	}
}

func TestRuntimeDirUsesXDGRuntimeDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	if got, want := RuntimeDir(1234), filepath.Join(dir, "voom"); got != want {
		t.Fatalf("RuntimeDir = %s, want %s", got, want)
	}
}

func TestExeOverrideName(t *testing.T) {
	cases := map[string]string{
		"gvproxy":          "VOOM_GVPROXY",
		"virtiofsd":        "VOOM_VIRTIOFSD",
		"qemu-system-test": "VOOM_QEMU_SYSTEM_TEST",
	}
	for name, want := range cases {
		if got := ExeOverrideName(name); got != want {
			t.Fatalf("ExeOverrideName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestExePathUsesOverride(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "tool")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ExeOverrideName("qemu-img"), exe)
	got, err := ExePath("qemu-img")
	if err != nil {
		t.Fatal(err)
	}
	if got != exe {
		t.Fatalf("ExePath override = %s, want %s", got, exe)
	}
}

func TestValidateHostImageRejectsUnsupportedDriver(t *testing.T) {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x86_64"
	}
	if arch == "arm64" {
		arch = "aarch64"
	}
	if err := ValidateHostImage(arch+"-linux", "qcow2", "unknown"); err == nil {
		t.Fatalf("expected unsupported driver error")
	}
}

func TestValidateHostImageRejectsVfkitNonRaw(t *testing.T) {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x86_64"
	}
	if arch == "arm64" {
		arch = "aarch64"
	}
	err := ValidateHostImage(arch+"-linux", "qcow2", "vfkit")
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		if err == nil || err.Error() != "vfkit requires raw image/disk format, got qcow2" {
			t.Fatalf("unexpected vfkit format validation error: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("expected vfkit host validation failure off macOS arm64")
	}
}
