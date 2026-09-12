package process

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStartRecordedWritesValidRecord(t *testing.T) {
	dir := t.TempDir()
	recordPath := filepath.Join(dir, "helper.process.json")
	if err := StartRecorded("bash", []string{"-c", "exec -a gvproxy bash -c \"trap 'exit 0' TERM; while true; do sleep 1; done\""}, filepath.Join(dir, "helper.log"), recordPath, exec.LookPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = StopRecorded(recordPath, "gvproxy", time.Second) })
	if err := WaitFor(func() bool { _, ok := ValidRecord(recordPath, "gvproxy"); return ok }, time.Second); err != nil {
		t.Fatal("recorded process did not become valid")
	}
	if err := StopRecorded(recordPath, "gvproxy", time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestStartRecordedStopsChildWhenRecordWriteFails(t *testing.T) {
	dir := t.TempDir()
	badParent := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(badParent, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	token := "voom-record-failure-" + filepath.Base(dir)
	err := StartRecorded(
		"bash",
		[]string{"-c", "trap '' TERM; while true; do sleep 1; done", token},
		filepath.Join(dir, "helper.log"),
		filepath.Join(badParent, "helper.process.json"),
		exec.LookPath,
	)
	if err == nil || !strings.Contains(err.Error(), "record process identity") {
		t.Fatalf("StartRecorded error = %v", err)
	}
	out, err := exec.Command("ps", "ax", "-o", "command=").Output()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte(token)) {
		t.Fatal("child remains after process record failure")
	}
}

func TestStartRecordedAppendsAfterConcurrentLogWriter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "helper.log")
	flag := filepath.Join(dir, "continue")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(dir, "helper.process.json")
	if err := StartRecorded("bash", []string{"-c", `printf first; while [ ! -e "$1" ]; do sleep 0.1; done; printf second`, "bash", flag, "forward", "auto", "watch", "test"}, path, recordPath, exec.LookPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = StopRecorded(recordPath, "auto-forward", time.Second) })
	if err := WaitFor(func() bool {
		b, _ := os.ReadFile(path)
		return string(b) == "first"
	}, 3*time.Second); err != nil {
		t.Fatalf("detached process did not write first message: %v", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("external"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WaitFor(func() bool {
		b, _ := os.ReadFile(path)
		return string(b) == "firstexternalsecond"
	}, 3*time.Second); err != nil {
		b, _ := os.ReadFile(path)
		t.Fatalf("log = %q", b)
	}
}

func TestValidRecordMatchesExpectedProcessKinds(t *testing.T) {
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
			recordPath := filepath.Join(t.TempDir(), "service.process.json")
			if err := Record(recordPath, cmd.Process.Pid); err != nil {
				t.Fatal(err)
			}
			if pid, ok := ValidRecord(recordPath, tc.kind); !ok || pid != cmd.Process.Pid {
				t.Fatalf("ValidRecord(%s) = %d, %t; want %d, true", tc.kind, pid, ok, cmd.Process.Pid)
			}
		})
	}
}

func TestValidRecordRejectsUnrelatedProcess(t *testing.T) {
	cmd := fakeNamedProcess(t, "not-voom-auto-forward")
	recordPath := filepath.Join(t.TempDir(), "service.process.json")
	if err := Record(recordPath, cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if _, ok := ValidRecord(recordPath, "auto-forward"); ok {
		t.Fatal("unrelated process validated as auto-forward")
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
