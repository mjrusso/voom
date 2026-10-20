package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecordedProcessKindsWithSpacedExecutablePath(t *testing.T) {
	if os.Getenv("VOOM_PROCESS_SPACE_CHILD") == "1" {
		time.Sleep(30 * time.Second)
		return
	}
	dir := filepath.Join(t.TempDir(), "voom run", strings.Repeat("nested/", 90), "Display.app", "Contents", "MacOS")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(dir, "vfkit")
	if err := os.WriteFile(executable, source, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		kind string
		args []string
		want bool
	}{
		{"driver", "vfkit", nil, true},
		{"runtime-watch", "runtime-watch", []string{"debug", "watch-vm", "vm-id"}, true},
		{"auto-forward", "auto-forward", []string{"forward", "auto", "watch", "vm-id"}, true},
		{"unrelated-runtime-watch", "runtime-watch", []string{"debug", "other"}, false},
		{"unrelated-auto-forward", "auto-forward", []string{"forward", "auto", "other"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"-test.run=^TestRecordedProcessKindsWithSpacedExecutablePath$", "--"}, tc.args...)
			cmd := exec.Command(executable, args...)
			cmd.Env = append(os.Environ(), "VOOM_PROCESS_SPACE_CHILD=1")
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
			record := filepath.Join(t.TempDir(), "vm.process.json")
			if err := Record(record, cmd.Process.Pid); err != nil {
				t.Fatal(err)
			}
			pid, ok := ValidRecord(record, tc.kind)
			if ok != tc.want || ok && pid != cmd.Process.Pid {
				t.Fatalf("ValidRecord(%s) = %d, %t; want recognized=%t", tc.kind, pid, ok, tc.want)
			}
			if err := StopRecorded(record, "vfkit", time.Second); err != nil {
				t.Fatal(err)
			}
		})
	}
}
