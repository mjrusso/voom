package vm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

func TestWatcherBoundsDiscoveryWhileGlobalLockIsHeld(t *testing.T) {
	st, m, vm, _ := newAutoForwardFixture(t)
	writeAutoForwardReport(t, st, vm, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now(), Listeners: []GuestListener{{Proto: "tcp", Addr: "0.0.0.0", Port: freeLoopbackPort(t)}}})
	fingerprint, err := process.RecordFingerprint(st.Runtime(vm).VMProcessRecord())
	if err != nil {
		t.Fatal(err)
	}
	locked, release, globalDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() { globalDone <- st.WithGlobal(func() error { close(locked); <-release; return nil }) }()
	<-locked
	var once sync.Once
	unlockGlobal := func() {
		once.Do(func() {
			close(release)
			if err := <-globalDone; err != nil {
				t.Error(err)
			}
		})
	}
	defer unlockGlobal()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	schedule := runtimeWatchSchedule{nextDiscovery: time.Now()}
	done := make(chan error, 1)
	go func() { _, err := m.watchRuntimeOnce(ctx, vm.ID, fingerprint, &schedule); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			t.Fatalf("discovery did not use its own deadline: %v", err)
		}
	case <-time.After(3 * time.Second):
		cancel()
		unlockGlobal()
		<-done
		t.Fatal("discovery retained the VM lock without a bounded attempt")
	}
	unlock, err := st.TryLockVM(vm.ID)
	if err != nil || unlock == nil {
		t.Fatalf("discovery deadline retained VM lock: %v", err)
	}
	unlock()
	exitAutoForwardFixtureDriver(t, st, vm)
	unlockGlobal()
	if outcome, err := m.watchRuntimeOnce(ctx, vm.ID, fingerprint, &schedule); outcome != runtimeWatchDone || err != nil {
		t.Fatalf("discovery deadline prevented exit cleanup: %v, %v", outcome, err)
	}
}

func TestExitedRecoveryHonorsCallerDeadlineWhileStorageIsLocked(t *testing.T) {
	st, vm, _ := macosResetFixture(t)
	m := New(st)
	rt := st.Runtime(vm)
	if err := os.MkdirAll(rt.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
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
	if err := state.WriteJSONAtomic(rt.ForwardMutation(), []forward.Decl{{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 18080, GuestPort: 8080}}); err != nil {
		t.Fatal(err)
	}
	disk, err := os.Open(filepath.Join(st.VMStorageDir(vm), "disk.raw"))
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(disk.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = disk.Close()
		t.Fatal(err)
	}
	var once sync.Once
	unlockStorage := func() { once.Do(func() { _ = disk.Close() }) }
	defer unlockStorage()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := m.ReconcileForwards(ctx, vm.Name); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("caller deadline was lost: %v", err)
		}
	case <-time.After(time.Second):
		unlockStorage()
		<-done
		t.Fatal("exit recovery ignored the caller's reconciliation deadline")
	}
	if !process.HasRecord(rt.VMProcessRecord()) {
		t.Fatal("unfinished cleanup lost the driver identity")
	}
	unlock, err := st.TryLockVM(vm.ID)
	if err != nil || unlock == nil {
		t.Fatalf("deadline retained the VM lock: %v", err)
	}
	unlock()
	unlockStorage()
	if outcome, err := m.watchRuntimeOnce(context.Background(), vm.ID, fingerprint, &runtimeWatchSchedule{}); outcome != runtimeWatchDone || err != nil {
		t.Fatalf("watcher did not finish deferred cleanup: %v, %v", outcome, err)
	}
}
