package vm

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

func lifecycleTestStore(t *testing.T) *state.Store {
	t.Helper()
	st, _ := newTestStore(t)
	for _, name := range []string{"a", "b"} {
		record := &state.VMRecord{SchemaVersion: 1, ID: name, Name: name, Driver: "qemu", Image: state.VMImageRef{ID: "image1"}, UpdatedAt: time.Now().UTC()}
		if err := os.MkdirAll(st.VMDir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveVM(record); err != nil {
			t.Fatal(err)
		}
		if err := st.RegisterVM(name, name); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func TestCleanupAttemptsGatewayAfterOtherHelperFailure(t *testing.T) {
	st, _ := newTestStore(t)
	record := &state.VMRecord{ID: "cleanup", Name: "cleanup", Driver: "qemu"}
	rt := st.Runtime(record)
	if err := os.MkdirAll(rt.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := fakeNamedVMProcess(t, "gvproxy")
	writeProcessRecord(t, rt.GVProxyProcessRecord(), cmd)
	if err := os.WriteFile(rt.AutoForwardProcessRecord(), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	stop, err := New(st).stopRuntime(context.Background(), record)
	if err == nil || !stop.changed {
		t.Fatal("unknown helper identity was accepted")
	}
	if !stop.gatewayTerminationConfirmed {
		t.Fatal("unrelated helper failure obscured confirmed gateway termination")
	}
	if _, err := os.Stat(rt.GVProxyProcessRecord()); !os.IsNotExist(err) {
		t.Fatal("gateway termination was not attempted")
	}
	if _, err := os.Stat(rt.AutoForwardProcessRecord()); err != nil {
		t.Fatal("unknown helper record was discarded")
	}
}

func TestCleanupFindsVirtiofsProcessRecord(t *testing.T) {
	st, _ := newTestStore(t)
	record := &state.VMRecord{ID: "cleanup", Name: "cleanup", Driver: "qemu"}
	rt := st.Runtime(record)
	if err := os.MkdirAll(rt.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := fakeNamedVMProcess(t, "virtiofsd")
	recordPath := filepath.Join(rt.Dir(), "virtiofs-test.process.json")
	if err := process.Record(recordPath, cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(rt.Dir(), "virtiofs-test.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	lockPath := socketPath + ".pid"
	if err := os.WriteFile(lockPath, []byte("123\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stop, err := New(st).stopRuntime(context.Background(), record)
	if err != nil || !stop.changed {
		t.Fatalf("stopRuntime result=%+v error=%v", stop, err)
	}
	_, _ = cmd.Process.Wait()
	if process.Alive(cmd.Process.Pid) {
		t.Fatal("virtiofsd process remains")
	}
	if process.HasRecord(recordPath) {
		t.Fatal("virtiofsd process record remains")
	}
	for _, path := range []string{socketPath, lockPath} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("virtiofsd artifact remains at %s: %v", path, err)
		}
	}
}

func TestCleanupRetainsVirtiofsArtifactsWithoutProcessRecord(t *testing.T) {
	st, _ := newTestStore(t)
	record := &state.VMRecord{ID: "cleanup", Name: "cleanup", Driver: "qemu"}
	rt := st.Runtime(record)
	if err := os.MkdirAll(rt.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(rt.Dir(), "virtiofs-test.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	lockPath := socketPath + ".pid"
	if err := os.WriteFile(lockPath, []byte("123\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stop, err := New(st).stopRuntime(context.Background(), record)
	if err == nil || !strings.Contains(err.Error(), "virtiofsd termination unconfirmed") || !stop.changed {
		t.Fatalf("stopRuntime result=%+v error=%v", stop, err)
	}
	for _, path := range []string{socketPath, lockPath} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("virtiofsd artifact was removed from %s: %v", path, err)
		}
	}
}

func TestWaitingForVMLockDoesNotHoldGlobal(t *testing.T) {
	st := lifecycleTestStore(t)
	local, err := st.LockVM("a")
	if err != nil {
		t.Fatal(err)
	}
	defer local()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	waiting := make(chan error, 1)
	go func() { err := New(st).Remove(ctx, "a"); waiting <- err }()
	// Give the blocked command an opportunity to acquire the global lock.
	time.Sleep(50 * time.Millisecond)
	other := make(chan error, 1)
	go func() { err := New(st).Remove(context.Background(), "b"); other <- err }()
	select {
	case err := <-other:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated VM blocked by per-VM lock")
	}
	select {
	case err := <-waiting:
		if err == nil {
			t.Fatal("ignored cancellation while waiting for VM")
		}
	case <-time.After(time.Second):
		t.Fatal("VM lock wait ignored cancellation")
	}
}

func TestCleanupReleasesGlobal(t *testing.T) {
	for _, action := range []string{"remove", "reset"} {
		t.Run(action, func(t *testing.T) {
			st := lifecycleTestStore(t)
			record, err := st.LoadVM("a")
			if err != nil {
				t.Fatal(err)
			}
			rt := st.Runtime(record)
			if err := os.MkdirAll(rt.Dir(), 0700); err != nil {
				t.Fatal(err)
			}
			driver := fakeNamedVMProcess(t, "qemu-system-test")
			writeProcessRecord(t, rt.VMProcessRecord(), driver)
			monitor, err := net.Listen("unix", rt.QEMUMonitor())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = monitor.Close() }()
			entered := make(chan struct{})
			go func() {
				conn, err := monitor.Accept()
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				data := make([]byte, 64)
				if _, err := conn.Read(data); err == nil {
					close(entered)
				}
			}()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			manager := New(st)
			go func() {
				if action == "remove" {
					done <- manager.Remove(ctx, "a")
				} else {
					done <- manager.ResetDisk(ctx, "a", "missing-image")
				}
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("cleanup did not reach graceful shutdown")
			}
			other := make(chan error, 1)
			go func() { err := manager.Remove(context.Background(), "b"); other <- err }()
			select {
			case err := <-other:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("cleanup blocked unrelated configuration")
			}
			cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("expected cancelled removal or missing reset image")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cleanup did not finish")
			}
		})
	}
}
