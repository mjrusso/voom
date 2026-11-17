package process

import (
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

func TestLaunchWaitsForIdentityPublication(t *testing.T) {
	if dir := os.Getenv("VOOM_LAUNCH_TEST_DIR"); dir != "" {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := os.WriteFile(filepath.Join(dir, "parent"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			t.Fatal(err)
		}
		err := StartRecorded(testExecutable(t, "bash"), []string{"-c", `printf started > "$1"; exec -a gvproxy sleep 60`, "bash", filepath.Join(dir, "executed")}, filepath.Join(dir, "log"), filepath.Join(dir, "record"))
		if err == nil {
			t.Fatal("identity publication unexpectedly succeeded")
		}
		return
	}
	strace, err := exec.LookPath("strace")
	if err != nil {
		t.Skip("strace is required for launch fault injection")
	}
	for _, crash := range []bool{false, true} {
		t.Run(map[bool]string{false: "publication fails", true: "parent dies"}[crash], func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "record"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "record", "keep"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(strace, "-f", "-o", filepath.Join(dir, "trace"), "-e", "trace=fsync", "-e", "inject=fsync:delay_enter=1s:when=1", os.Args[0], "-test.run=^TestLaunchWaitsForIdentityPublication$")
			cmd.Env = append(os.Environ(), "VOOM_LAUNCH_TEST_DIR="+dir)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			if crash {
				var parent, child int
				deadline := time.Now().Add(5 * time.Second)
				for child == 0 {
					if data, err := os.ReadFile(filepath.Join(dir, "parent")); err == nil {
						parent, _ = strconv.Atoi(string(data))
					}
					if parent != 0 {
						paths, _ := filepath.Glob(filepath.Join("/proc", strconv.Itoa(parent), "task", "*", "children"))
						for _, path := range paths {
							data, _ := os.ReadFile(path)
							if fields := strings.Fields(string(data)); len(fields) != 0 {
								child, _ = strconv.Atoi(fields[0])
							}
						}
					}
					if time.Now().After(deadline) {
						t.Fatal("launcher did not create a child")
					}
					time.Sleep(5 * time.Millisecond)
				}
				t.Cleanup(func() { _ = syscall.Kill(-child, syscall.SIGKILL) })
				if err := syscall.Kill(parent, syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				if !crash && err != nil {
					t.Fatalf("launcher failed: %v", err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("child survived without a published identity")
			}
			if _, err := os.Stat(filepath.Join(dir, "executed")); !os.IsNotExist(err) {
				t.Fatalf("payload ran before identity publication: %v", err)
			}
		})
	}
}
