package vm

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/process"
)

func TestRuntimeWatcherKeepsPollingAfterDiscoveryErrors(t *testing.T) {
	for _, change := range []string{"report", "driver-exit"} {
		t.Run(change, func(t *testing.T) {
			st, m, vm, _ := newAutoForwardFixture(t)
			rt := st.Runtime(vm)
			fingerprint, err := process.RecordFingerprint(rt.VMProcessRecord())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			done := make(chan struct{})
			var watcherErr error
			go func() { defer close(done); watcherErr = m.WatchRuntime(ctx, vm.ID, fingerprint) }()
			t.Cleanup(func() { cancel(); <-done })
			waitError := func(message string) {
				t.Helper()
				if err := process.WaitFor(func() bool {
					data, _ := os.ReadFile(st.LogPath(vm, "auto-forward"))
					return strings.Contains(string(data), message)
				}, 3*time.Second); err != nil {
					t.Fatalf("watcher did not report %q: %v", message, err)
				}
			}
			waitError("guest port report unavailable")
			if err := os.WriteFile(st.ControlSharePortsPath(vm), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			waitError("invalid character")
			if change == "driver-exit" {
				exitAutoForwardFixtureDriver(t, st, vm)
				select {
				case <-done:
					if watcherErr != nil {
						t.Fatal(watcherErr)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("discovery errors delayed driver-exit cleanup")
				}
				if process.HasRecord(rt.GVProxyProcessRecord()) || process.HasRecord(rt.VMProcessRecord()) {
					t.Fatal("driver-exit cleanup left runtime helpers behind")
				}
				return
			}
			port := freeLoopbackPort(t)
			for port == vm.Network.SSHPort {
				port = freeLoopbackPort(t)
			}
			writeAutoForwardReport(t, st, vm, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now(), Listeners: []GuestListener{{Proto: "tcp", Addr: "0.0.0.0", Port: port}}})
			if err := process.WaitFor(func() bool {
				rows, err := m.ReadRuntimeAutoForwards(vm)
				return err == nil && len(rows) == 1 && rows[0].HostPort == port && rows[0].Installed()
			}, 3*time.Second); err != nil {
				t.Fatal("discovery errors delayed the new report: ", err)
			}
			cancel()
			<-done
			if !errors.Is(watcherErr, context.Canceled) {
				t.Fatalf("watcher stopped before cancellation: %v", watcherErr)
			}
		})
	}
}

func TestRuntimeWatcherCleansExitDuringRecoveryBackoff(t *testing.T) {
	st, m, vm, proxy := newAutoForwardFixture(t)
	proxy.dropNextResponses(-1)
	if _, err := m.AddForward(context.Background(), vm.Name, 8080, freeLoopbackPort(t), "127.0.0.1", false, "tcp"); !errors.Is(err, errForwardRecoveryRequired) {
		t.Fatalf("lost response: %v", err)
	}
	proxy.mu.Lock()
	baseline := proxy.submissions
	proxy.mu.Unlock()
	rt := st.Runtime(vm)
	fingerprint, err := process.RecordFingerprint(rt.VMProcessRecord())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	done := make(chan struct{})
	var watcherErr error
	go func() { defer close(done); watcherErr = m.WatchRuntime(ctx, vm.ID, fingerprint) }()
	t.Cleanup(func() { cancel(); <-done })
	if err := process.WaitFor(func() bool {
		proxy.mu.Lock()
		defer proxy.mu.Unlock()
		return proxy.submissions >= baseline+2
	}, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	exitAutoForwardFixtureDriver(t, st, vm)
	select {
	case <-done:
		if watcherErr != nil {
			t.Fatal(watcherErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forward recovery backoff delayed exit cleanup")
	}
	if m.forwardMutationPending(vm) || process.HasRecord(rt.VMProcessRecord()) || process.HasRecord(rt.GVProxyProcessRecord()) {
		t.Fatal("confirmed driver exit retained recovery state")
	}
}

func TestRuntimeWatcherReconcilesAutomaticSettings(t *testing.T) {
	st, m, vm, proxy := newAutoForwardFixture(t)
	port := freeLoopbackPort(t)
	for port == vm.Network.SSHPort {
		port = freeLoopbackPort(t)
	}
	writeAutoForwardReport(t, st, vm, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now(), Listeners: []GuestListener{{Proto: "tcp", Addr: "0.0.0.0", Port: port}}})
	fingerprint, err := process.RecordFingerprint(st.Runtime(vm).VMProcessRecord())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	done := make(chan struct{})
	var watcherErr error
	go func() { defer close(done); watcherErr = m.WatchRuntime(ctx, vm.ID, fingerprint) }()
	t.Cleanup(func() { cancel(); <-done })
	waitInstalled := func(port int) {
		t.Helper()
		if err := process.WaitFor(func() bool {
			rows, err := m.ReadRuntimeAutoForwards(vm)
			return err == nil && len(rows) == 1 && rows[0].HostPort == port && rows[0].Installed()
		}, 3*time.Second); err != nil {
			t.Fatal("runtime watcher did not reconcile automatic forwarding: ", err)
		}
	}
	waitInstalled(port)
	enabled := false
	if _, err := m.UpdateAutoForward(context.Background(), vm.Name, AutoForwardUpdate{Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	next := freeLoopbackPort(t)
	for next == port || next == vm.Network.SSHPort {
		next = freeLoopbackPort(t)
	}
	writeAutoForwardReport(t, st, vm, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now(), Listeners: []GuestListener{{Proto: "tcp", Addr: "0.0.0.0", Port: next}}})
	enabled = true
	if _, err := m.UpdateAutoForward(context.Background(), vm.Name, AutoForwardUpdate{Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	waitInstalled(next)
	cancel()
	<-done
	if !errors.Is(watcherErr, context.Canceled) {
		t.Fatalf("watcher ended before cancellation: %v", watcherErr)
	}
	if exposes, unexposes := proxy.counts(); exposes != 2 || unexposes != 1 {
		t.Fatalf("automatic reconciliation: expose=%d unexpose=%d", exposes, unexposes)
	}
}
