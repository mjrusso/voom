package vm

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

func TestRejectedAddAfterCLIExit(t *testing.T) {
	if os.Getenv("VOOM_REJECTED_ADD_CHILD") == "1" {
		st, err := state.Open()
		if err != nil {
			t.Fatal(err)
		}
		port, err := strconv.Atoi(os.Getenv("VOOM_RECOVERY_PORT"))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = New(st).AddForward(context.Background(), "demo", 8081, port, "127.0.0.1", false, "tcp")
		return
	}
	st, m, vm, proxy := newAutoForwardFixture(t)
	priorPort := freeLoopbackPort(t)
	for priorPort == vm.Network.SSHPort {
		priorPort = freeLoopbackPort(t)
	}
	prior, err := m.AddForward(context.Background(), vm.Name, 8080, priorPort, "127.0.0.1", false, "tcp")
	if err != nil {
		t.Fatal(err)
	}
	port := freeLoopbackPort(t)
	for port == prior.HostPort || port == vm.Network.SSHPort {
		port = freeLoopbackPort(t)
	}
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	entered := make(chan string, 1)
	proxy.mu.Lock()
	proxy.exposeStatus = 409
	proxy.delayAction, proxy.release, proxy.entered = "expose", release, entered
	proxy.mu.Unlock()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestRejectedAddAfterCLIExit$")
	cmd.Env = append(os.Environ(), "VOOM_REJECTED_ADD_CHILD=1", "VOOM_RECOVERY_PORT="+strconv.Itoa(port))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	var id string
	select {
	case id = <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("add did not reach gvproxy")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	releaseOnce.Do(func() { close(release) })
	if err := process.WaitFor(func() bool {
		proxy.mu.Lock()
		defer proxy.mu.Unlock()
		return proxy.finished[id]
	}, time.Second); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := process.RecordFingerprint(st.Runtime(vm).VMProcessRecord())
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := New(st).watchRuntimeOnce(context.Background(), vm.ID, fingerprint, &runtimeWatchSchedule{})
	if outcome == runtimeWatchSkipped || err == nil || !strings.Contains(err.Error(), "409") || m.forwardMutationPending(vm) {
		t.Fatalf("recovered rejection: outcome=%d pending=%t err=%v", outcome, m.forwardMutationPending(vm), err)
	}
	saved, err := st.LoadVM(vm.Name)
	if err != nil || len(saved.Network.Forwards) != 1 || saved.Network.Forwards[0] != prior {
		t.Fatalf("rejected add changed declarations: %+v %v", saved, err)
	}
	if reserved, err := st.PortReserved("tcp", "0.0.0.0", port); err != nil || reserved {
		t.Fatalf("rejected add retained reservation: %t %v", reserved, err)
	}
	if !m.IsRunning(vm) {
		t.Fatal("recovery stopped the VM")
	}
}

func TestRuntimeWatcherRetriesInspectionFailure(t *testing.T) {
	st, m, vm, proxy := newAutoForwardFixture(t)
	proxy.dropNextResponses(-1)
	port := freeLoopbackPort(t)
	if _, err := m.AddForward(context.Background(), vm.Name, 8080, port, "127.0.0.1", false, "tcp"); err == nil {
		t.Fatal("lost response reported success")
	}
	rt := st.Runtime(vm)
	record := rt.VMProcessRecord()
	fingerprint, err := process.RecordFingerprint(record)
	if err != nil {
		t.Fatal(err)
	}
	backup := record + ".backup"
	if err := os.Rename(record, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(record, 0o700); err != nil {
		t.Fatal(err)
	}
	restore := func() {
		t.Helper()
		if err := os.Remove(record); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(backup, record); err != nil {
			t.Fatal(err)
		}
	}
	var restoreOnce sync.Once
	t.Cleanup(func() { restoreOnce.Do(restore) })
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	done := make(chan struct{})
	var watcherErr error
	go func() {
		defer close(done)
		watcherErr = New(st).WatchRuntime(ctx, vm.ID, fingerprint)
	}()
	t.Cleanup(func() { cancel(); <-done })
	if err := process.WaitFor(func() bool {
		data, _ := os.ReadFile(st.LogPath(vm, "auto-forward"))
		return strings.Contains(string(data), "is a directory")
	}, time.Second); err != nil {
		select {
		case <-done:
			t.Fatalf("watcher exited on inspection failure: %v", watcherErr)
		default:
			t.Fatal(err)
		}
	}
	if reserved, err := st.PortReserved("tcp", "0.0.0.0", port); err != nil || !reserved || !m.forwardMutationPending(vm) {
		t.Fatalf("inspection failure released state: reserved=%t err=%v", reserved, err)
	}
	proxy.mu.Lock()
	clear(proxy.dropResponses)
	proxy.mu.Unlock()
	restoreOnce.Do(restore)
	if err := process.WaitFor(func() bool { return !m.forwardMutationPending(vm) }, 5*time.Second); err != nil {
		t.Fatal("watcher did not resume recovery: ", err)
	}
	cancel()
	<-done
	if !errors.Is(watcherErr, context.Canceled) {
		t.Fatalf("watcher stopped before cancellation: %v", watcherErr)
	}
	if !m.IsRunning(vm) {
		t.Fatal("inspection failure stopped the VM")
	}
	if exposes, _ := proxy.counts(); exposes != 1 {
		t.Fatalf("recovery repeated expose: %d", exposes)
	}
}

func TestRemoveSettlesRejectedAdd(t *testing.T) {
	st, m, vm, proxy := newAutoForwardFixture(t)
	proxy.exposeStatus = 409
	proxy.dropNextResponses(-1)
	port := freeLoopbackPort(t)
	if _, err := m.AddForward(context.Background(), vm.Name, 8080, port, "127.0.0.1", false, "tcp"); !errors.Is(err, errForwardRecoveryRequired) {
		t.Fatalf("unconfirmed rejection: %v", err)
	}
	proxy.mu.Lock()
	clear(proxy.dropResponses)
	proxy.mu.Unlock()
	if err := m.RemoveForward(context.Background(), vm.Name, port, "127.0.0.1", "tcp"); err != nil {
		t.Fatalf("removing rejected add: %v", err)
	}
	if reserved, err := st.PortReserved("tcp", "0.0.0.0", port); err != nil || reserved {
		t.Fatalf("rejected add retained reservation: %t %v", reserved, err)
	}
	if exposes, unexposes := proxy.counts(); exposes != 1 || unexposes != 0 {
		t.Fatalf("rejected add was mutated again: expose=%d unexpose=%d", exposes, unexposes)
	}
}
