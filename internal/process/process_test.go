package process

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestStartDetachedPreservesReleasedProcessPID(t *testing.T) {
	cmd, err := StartDetached("bash", []string{"-c", "trap 'exit 0' TERM; while true; do sleep 1; done"}, filepath.Join(t.TempDir(), "helper.log"), exec.LookPath)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Process == nil || cmd.Process.Pid <= 0 || !Alive(cmd.Process.Pid) {
		t.Fatalf("detached process pid was not preserved: %#v", cmd.Process)
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	_ = WaitFor(func() bool { return !Alive(cmd.Process.Pid) }, 3*time.Second)
}

func TestValidPIDMatchesExpectedProcessKinds(t *testing.T) {
	cases := []struct {
		name string
		kind string
		args []string
	}{
		{"qemu-system-test", "qemu", nil},
		{"gvproxy-test", "gvproxy", nil},
		{"virtiofsd-test", "virtiofsd", nil},
		{"voom-test", "auto-forward", []string{"forward", "auto", "watch", "scratch"}},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			cmd := fakeNamedProcess(t, tc.name, tc.args...)
			pidfile := filepath.Join(t.TempDir(), "service.pid")
			if err := os.WriteFile(pidfile, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if pid, ok := ValidPID(pidfile, tc.kind); !ok || pid != cmd.Process.Pid {
				t.Fatalf("ValidPID(%s) = %d, %t; want %d, true", tc.kind, pid, ok, cmd.Process.Pid)
			}
		})
	}
}

func TestValidPIDRejectsUnrelatedProcess(t *testing.T) {
	cmd := fakeNamedProcess(t, "not-voom-auto-forward")
	pidfile := filepath.Join(t.TempDir(), "service.pid")
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := ValidPID(pidfile, "auto-forward"); ok {
		t.Fatal("unrelated process validated as auto-forward")
	}
}

func TestStopPidfileDoesNotKillMismatchedProcess(t *testing.T) {
	cmd := fakeNamedProcess(t, "unrelated")
	pidfile := filepath.Join(t.TempDir(), "service.pid")
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	StopPidfile(pidfile, "gvproxy", 100*time.Millisecond)
	if !Alive(cmd.Process.Pid) {
		t.Fatal("mismatched process was killed")
	}
	if _, err := os.Stat(pidfile); !os.IsNotExist(err) {
		t.Fatalf("pidfile was not removed: %v", err)
	}
}

func fakeNamedProcess(t *testing.T, name string, args ...string) *exec.Cmd {
	t.Helper()
	payload := "exec -a " + name + " bash -c 'trap \"exit 0\" TERM; while true; do sleep 1; done' " + stringsForShell(args)
	cmd := exec.Command("bash", "-c", payload)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := WaitFor(func() bool {
		if runtime.GOOS == "linux" {
			cmdline, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "cmdline"))
			first := string(bytes.Split(cmdline, []byte{0})[0])
			return filepath.Base(first) == name
		}
		out, _ := exec.Command("ps", "-p", strconv.Itoa(cmd.Process.Pid), "-o", "command=").Output()
		return len(out) > 0 && filepath.Base(strings.Fields(string(out))[0]) == name
	}, time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("fake process did not assume name %s", name)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

func stringsForShell(args []string) string {
	out := ""
	for _, arg := range args {
		out += " '" + arg + "'"
	}
	return out
}
