package share

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewDeclValidatesAndAbsolutizes(t *testing.T) {
	hostDir := t.TempDir()
	decl, err := NewDecl("src", hostDir, "/mnt/src", "voom-control", true)
	if err != nil {
		t.Fatal(err)
	}
	if decl.Tag != "src" || decl.HostPath != hostDir || decl.GuestPath != "/mnt/src" || !decl.Readonly {
		t.Fatalf("unexpected decl: %#v", decl)
	}
	if _, err := NewDecl("voom-control", hostDir, "/mnt/src", "voom-control", false); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected reserved tag error, got %v", err)
	}
	if _, err := NewDecl("src", hostDir, "relative", "voom-control", false); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("expected guest path error, got %v", err)
	}
	if _, err := NewDecl("src", filepath.Join(hostDir, "missing"), "/mnt/src", "voom-control", false); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected host path error, got %v", err)
	}
}

func TestRuntimeAndVirtiofsdArgs(t *testing.T) {
	rtDir := t.TempDir()
	decl := Decl{Tag: "repo", HostPath: "/work/repo", GuestPath: "/mnt/repo", Readonly: true}
	rt, err := RuntimeForVM(rtDir, decl)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Sock != filepath.Join(rtDir, "virtiofs-repo.sock") || rt.Pidfile != filepath.Join(rtDir, "virtiofs-repo.pid") || rt.LogKind != "virtiofs-repo" {
		t.Fatalf("unexpected runtime: %#v", rt)
	}
	args := VirtiofsdArgs(rt)
	for _, want := range []string{"--shared-dir", "/work/repo", "--socket-path", rt.Sock, "--readonly"} {
		if !hasArg(args, want) {
			t.Fatalf("args missing %q: %#v", want, args)
		}
	}
}

func TestControlRuntime(t *testing.T) {
	rtDir := t.TempDir()
	controlDir := filepath.Join(rtDir, "control")
	rt, err := ControlRuntime(rtDir, controlDir, "voom-control")
	if err != nil {
		t.Fatal(err)
	}
	if rt.Tag != "voom-control" || rt.HostPath != controlDir || rt.LogKind != "virtiofs-voom-control" {
		t.Fatalf("unexpected control runtime: %#v", rt)
	}
}

func TestGuestScripts(t *testing.T) {
	rt := Runtime{Tag: "repo", GuestPath: "/mnt/repo", Readonly: true}
	mount := GuestMountScript(rt)
	for _, want := range []string{"nsenter --mount=/proc/1/ns/mnt", "mkdir -p \"$target\"", "mountpoint -q \"$target\"", "mount -t virtiofs -o ro \"$tag\" \"$target\""} {
		if !strings.Contains(mount, want) {
			t.Fatalf("mount script missing %q:\n%s", want, mount)
		}
	}
	unmount := GuestUnmountScript(Decl{Tag: "repo", GuestPath: "/mnt/repo"})
	if !strings.Contains(unmount, "umount \"$target\"") || !strings.Contains(unmount, "mountpoint -q \"$target\"") {
		t.Fatalf("bad unmount script:\n%s", unmount)
	}
}

func TestSocketPathLengthValidation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux Unix socket path limit is Linux-specific")
	}
	err := ValidateUnixSocketPath("share src", filepath.Join("/", strings.Repeat("a", 120), "virtiofs-src.sock"))
	if err == nil || !strings.Contains(err.Error(), "socket path is too long") {
		t.Fatalf("expected socket path error, got %v", err)
	}
}

func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
