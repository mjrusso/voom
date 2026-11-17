package vm

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/mjrusso/voom/internal/state"
)

func TestRestartRejectsSurvivingDriver(t *testing.T) {
	if os.Getenv("VOOM_RESTART_HELPER") == "1" {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		st, err := state.Open()
		if err != nil {
			t.Fatal(err)
		}
		m := New(st)
		_, err = m.Restart(context.Background(), io.Discard, "a")
		if !errors.Is(err, syscall.EPERM) {
			t.Fatalf("restart ignored failed termination: %v", err)
		}
		vm, err := st.LoadVM("a")
		if err != nil {
			t.Fatal(err)
		}
		if !m.IsRunning(vm) {
			t.Fatal("fault did not leave the driver running")
		}
		return
	}
	strace, err := exec.LookPath("strace")
	if err != nil {
		t.Skip("strace is required for termination fault injection")
	}
	st := lifecycleTestStore(t)
	vm, err := st.LoadVM("a")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(st.Runtime(vm).Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	writeProcessRecord(t, st.Runtime(vm).VMProcessRecord(), fakeNamedVMProcess(t, "qemu-system-test"))
	trace := filepath.Join(t.TempDir(), "kill.trace")
	cmd := exec.Command(strace, "-f", "-o", trace, "-e", "trace=kill", "-e", "inject=kill:error=EPERM:when=1", os.Args[0], "-test.run=^TestRestartRejectsSurvivingDriver$")
	cmd.Env = append(os.Environ(), "VOOM_RESTART_HELPER=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		data, _ := os.ReadFile(trace)
		t.Fatalf("termination fault: %v\n%s\n%s", err, out, data)
	}
}
