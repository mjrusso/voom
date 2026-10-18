package vm

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

func TestShowGUIUsesExistingRuntimeWithoutRestart(t *testing.T) {
	st, vm, _ := macosResetFixture(t)
	manager := New(st)
	if err := manager.ShowGUI(context.Background(), vm.Name); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("stopped VM display accepted: %v", err)
	}
	driver := fakeNamedVMProcess(t, "vfkit")
	writeProcessRecord(t, st.Runtime(vm).VMProcessRecord(), driver)
	listener, err := net.Listen("unix", st.Runtime(vm).VFKitRestSock())
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan string, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Method + " " + r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	if err := manager.ShowGUI(context.Background(), vm.Name); err != nil {
		t.Fatal(err)
	}
	if got := <-requests; got != "POST /vm/display" {
		t.Fatalf("wrong display request: %s", got)
	}
	if pid, ok := process.ValidRecord(st.Runtime(vm).VMProcessRecord(), "vfkit"); !ok || pid != driver.Process.Pid {
		t.Fatal("display request changed the driver process")
	}
	if err := vm.MacOS.ValidateStorage(st.VMStorageDir(vm)); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeWatcherCleansOnlyTheExitedDriverGeneration(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "exited", true: "replaced"}[replacement], func(t *testing.T) {
			st, vm, _ := macosResetFixture(t)
			driver := fakeNamedVMProcess(t, "vfkit")
			rt := st.Runtime(vm)
			writeProcessRecord(t, rt.VMProcessRecord(), driver)
			fingerprint, err := process.RecordFingerprint(rt.VMProcessRecord())
			if err != nil {
				t.Fatal(err)
			}
			gateway := fakeNamedVMProcess(t, "gvproxy")
			writeProcessRecord(t, rt.GVProxyProcessRecord(), gateway)
			if err := driver.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_, _ = driver.Process.Wait()
			if replacement {
				writeProcessRecord(t, rt.VMProcessRecord(), fakeNamedVMProcess(t, "vfkit"))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := New(st).WatchRuntime(ctx, vm.ID, fingerprint); err != nil {
				t.Fatal(err)
			}
			_, alive := process.ValidRecord(rt.GVProxyProcessRecord(), "gvproxy")
			if alive != replacement {
				t.Fatalf("gateway alive=%v replacement=%v", alive, replacement)
			}
			if !replacement && process.HasRecord(rt.VMProcessRecord()) {
				t.Fatal("exited driver record remains")
			}
			if err := vm.MacOS.ValidateStorage(st.VMStorageDir(vm)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRuntimeWatcherCancellationAndUnknownIdentityRetainState(t *testing.T) {
	st, vm, _ := macosResetFixture(t)
	rt := st.Runtime(vm)
	driver := fakeNamedVMProcess(t, "vfkit")
	writeProcessRecord(t, rt.VMProcessRecord(), driver)
	fingerprint, err := process.RecordFingerprint(rt.VMProcessRecord())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := New(st).WatchRuntime(ctx, vm.ID, fingerprint); !errors.Is(err, context.Canceled) {
		t.Fatalf("watcher ignored cancellation: %v", err)
	}
	if _, ok := process.ValidRecord(rt.VMProcessRecord(), "vfkit"); !ok {
		t.Fatal("cancellation stopped VM")
	}
	gateway := fakeNamedVMProcess(t, "gvproxy")
	writeProcessRecord(t, rt.GVProxyProcessRecord(), gateway)
	if err := os.WriteFile(rt.VMProcessRecord(), []byte("unknown identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	fingerprint, err = process.RecordFingerprint(rt.VMProcessRecord())
	if err != nil {
		t.Fatal(err)
	}
	if err := New(st).WatchRuntime(context.Background(), vm.ID, fingerprint); err == nil {
		t.Fatal("unknown driver identity accepted")
	}
	if !process.HasRecord(rt.VMProcessRecord()) {
		t.Fatal("unknown identity record removed")
	}
	if !process.Alive(driver.Process.Pid) {
		t.Fatal("inspection failure stopped the live driver")
	}
	if _, alive := process.ValidRecord(rt.GVProxyProcessRecord(), "gvproxy"); !alive {
		t.Fatal("inspection failure tore down the live VM's network")
	}
	if err := vm.MacOS.ValidateStorage(st.VMStorageDir(vm)); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeWatcherWaitsForVMLockBeforeCleanup(t *testing.T) {
	st, vm, _ := macosResetFixture(t)
	rt := st.Runtime(vm)
	driver := fakeNamedVMProcess(t, "vfkit")
	writeProcessRecord(t, rt.VMProcessRecord(), driver)
	fingerprint, err := process.RecordFingerprint(rt.VMProcessRecord())
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = driver.Process.Wait()
	unlock, err := st.TryLockVM(vm.ID)
	if err != nil || unlock == nil {
		t.Fatalf("lock: %v", err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := New(st).WatchRuntime(ctx, vm.ID, fingerprint); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("watcher did not respect lock: %v", err)
	}
	if _, err := os.Stat(st.VMDiskPath(vm)); err != nil {
		t.Fatal(err)
	}
	if !process.HasRecord(rt.VMProcessRecord()) {
		t.Fatal("watcher cleaned up without lock")
	}
}

func TestRuntimeWatcherSubprocessCleansUpWithoutStoppingItself(t *testing.T) {
	if os.Getenv("VOOM_RUNTIME_WATCH_CHILD") == "1" {
		st, err := state.Open()
		if err != nil {
			t.Fatal(err)
		}
		vm, err := st.LoadVM("mac")
		if err != nil {
			t.Fatal(err)
		}
		rt := st.Runtime(vm)
		if err := process.Record(rt.RuntimeWatcherProcessRecord(), os.Getpid()); err != nil {
			t.Fatal(err)
		}
		fingerprint, err := process.RecordFingerprint(rt.VMProcessRecord())
		if err != nil {
			t.Fatal(err)
		}
		if err := New(st).WatchRuntime(context.Background(), vm.ID, fingerprint); err != nil {
			t.Fatal(err)
		}
		return
	}
	st, vm, _ := macosResetFixture(t)
	rt := st.Runtime(vm)
	driver := fakeNamedVMProcess(t, "vfkit")
	writeProcessRecord(t, rt.VMProcessRecord(), driver)
	gateway := fakeNamedVMProcess(t, "gvproxy")
	writeProcessRecord(t, rt.GVProxyProcessRecord(), gateway)
	if err := driver.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = driver.Process.Wait()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, exe, "-test.run=^TestRuntimeWatcherSubprocessCleansUpWithoutStoppingItself$", "--", "debug", "watch-vm", vm.ID)
	child.Env = append(os.Environ(), "VOOM_RUNTIME_WATCH_CHILD=1")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("runtime watcher: %v\n%s", err, output)
	}
	for _, path := range []string{rt.VMProcessRecord(), rt.GVProxyProcessRecord(), rt.RuntimeWatcherProcessRecord()} {
		if process.HasRecord(path) {
			t.Errorf("runtime record remains: %s", path)
		}
	}
	if _, alive := process.ValidRecord(rt.GVProxyProcessRecord(), "gvproxy"); alive {
		t.Fatal("gateway survived runtime cleanup")
	}
}

func TestExitedRuntimeCleanupRetainsNetworkWhenDriverInspectionFails(t *testing.T) {
	st, vm, _ := macosResetFixture(t)
	rt := st.Runtime(vm)
	driver := fakeNamedVMProcess(t, "vfkit")
	writeProcessRecord(t, rt.VMProcessRecord(), driver)
	gateway := fakeNamedVMProcess(t, "gvproxy")
	writeProcessRecord(t, rt.GVProxyProcessRecord(), gateway)
	if err := os.WriteFile(rt.VMProcessRecord(), []byte("unknown identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := process.RecordFingerprint(rt.VMProcessRecord())
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := st.TryLockVM(vm.ID)
	if err != nil || unlock == nil {
		t.Fatalf("lock: %v", err)
	}
	defer unlock()
	if err := New(st).cleanupExitedRuntime(context.Background(), vm.ID, fingerprint); err == nil {
		t.Fatal("cleanup accepted uncertain driver state")
	}
	if !process.Alive(driver.Process.Pid) {
		t.Fatal("cleanup stopped the live driver")
	}
	if _, alive := process.ValidRecord(rt.GVProxyProcessRecord(), "gvproxy"); !alive {
		t.Fatal("cleanup stopped the network before confirming driver exit")
	}
}
