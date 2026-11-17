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

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

func TestReconcileDelayedForwardWithoutRestart(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual", true: "automatic"}[automatic], func(t *testing.T) {
			st, manager, vm, proxy := newAutoForwardFixture(t)
			vm.Network.AutoForward = automatic
			if err := st.SaveVM(vm); err != nil {
				t.Fatal(err)
			}
			port := freeLoopbackPort(t)
			release := make(chan struct{})
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			entered := make(chan string, 1)
			proxy.mu.Lock()
			proxy.delayAction, proxy.release, proxy.entered = "expose", release, entered
			proxy.mu.Unlock()
			if automatic {
				writeAutoForwardReport(t, st, vm, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now(), Listeners: []GuestListener{{Proto: "tcp", Addr: "0.0.0.0", Port: port}}})
			}
			driver, err := process.RecordFingerprint(st.Runtime(vm).VMProcessRecord())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				if automatic {
					_, err := manager.ReconcileForwards(ctx, vm.Name)
					result <- err
				} else {
					_, err := manager.AddForward(ctx, vm.Name, port, port, "127.0.0.1", false, "tcp")
					result <- err
				}
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("forwarding request did not arrive")
			}
			cancel()
			if err := <-result; !errors.Is(err, gvproxy.ErrForwardOutcomeUnknown) {
				t.Fatal(err)
			}
			journal, err := forward.ReadMutationJournal(st.Runtime(vm).ForwardMutation())
			if err != nil || journal == nil || len(journal.Requests) != 1 || journal.Requests[0].ID == "" || journal.Requests[0].Result.Phase != forward.MutationUnknown {
				t.Fatalf("durable request: %+v %v", journal, err)
			}
			if reserved, err := st.PortReserved("tcp", "0.0.0.0", port); err != nil || !reserved {
				t.Fatalf("pending reservation: %t %v", reserved, err)
			}
			if _, err := New(st).ReconcileForwards(context.Background(), vm.Name); !errors.Is(err, errForwardRecoveryRequired) {
				t.Fatalf("pending request reported complete: %v", err)
			}
			releaseOnce.Do(func() { close(release) })
			if err := process.WaitFor(func() bool {
				proxy.mu.Lock()
				defer proxy.mu.Unlock()
				return proxy.finished[journal.Requests[0].ID]
			}, time.Second); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				rows, err := New(st).ReconcileForwards(context.Background(), vm.Name)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, row := range rows {
					if row.HostPort == port && row.Installed {
						found = true
					}
				}
				if !found {
					t.Fatalf("recovered forward missing: %+v", rows)
				}
			}
			if manager.forwardMutationPending(vm) {
				t.Fatal("completed recovery retained its journal")
			}
			if exposes, _ := proxy.counts(); exposes != 1 {
				t.Fatalf("original request was repeated: %d exposes", exposes)
			}
			if current, err := process.RecordFingerprint(st.Runtime(vm).VMProcessRecord()); err != nil || current != driver || !manager.IsRunning(vm) {
				t.Fatalf("recovery changed driver: %v", err)
			}
			proxy.mu.Lock()
			defer proxy.mu.Unlock()
			if !proxy.acknowledged[journal.Requests[0].ID] {
				t.Fatal("completed outcome was not acknowledged")
			}
		})
	}
}

func TestRuntimeWatcherRecoversManualForward(t *testing.T) {
	for _, driver := range []string{"qemu", "vfkit"} {
		t.Run(driver, func(t *testing.T) {
			st, manager, vm, proxy := newAutoForwardFixture(t)
			vm.Driver, vm.Network.AutoForward = driver, false
			if err := st.SaveVM(vm); err != nil {
				t.Fatal(err)
			}
			if driver == "vfkit" {
				writeProcessRecord(t, st.Runtime(vm).VMProcessRecord(), fakeNamedVMProcess(t, "vfkit"))
			}
			proxy.mu.Lock()
			proxy.dropResponses = map[string]int{"1": -1}
			proxy.mu.Unlock()
			port := freeLoopbackPort(t)
			if _, err := manager.AddForward(context.Background(), vm.Name, port, port, "127.0.0.1", false, "tcp"); !errors.Is(err, errForwardRecoveryRequired) {
				t.Fatal(err)
			}
			proxy.mu.Lock()
			proxy.dropResponses = nil
			proxy.mu.Unlock()
			fingerprint, err := process.RecordFingerprint(st.Runtime(vm).VMProcessRecord())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- New(st).WatchRuntime(ctx, vm.ID, fingerprint) }()
			if err := process.WaitFor(func() bool { return !manager.forwardMutationPending(vm) }, 3*time.Second); err != nil {
				t.Fatal(err)
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("watcher exit: %v", err)
			}
			if !manager.IsRunning(vm) {
				t.Fatal("background recovery stopped the VM")
			}
			if exposes, _ := proxy.counts(); exposes != 1 {
				t.Fatalf("background recovery repeated request: %d", exposes)
			}
		})
	}
}

func TestReconcileRemovalAfterCLIExit(t *testing.T) {
	if os.Getenv("VOOM_RECOVERY_CHILD") == "1" {
		st, err := state.Open()
		if err != nil {
			t.Fatal(err)
		}
		port, err := strconv.Atoi(os.Getenv("VOOM_RECOVERY_PORT"))
		if err != nil {
			t.Fatal(err)
		}
		if err := New(st).RemoveForward(context.Background(), "demo", port, "127.0.0.1", "tcp"); err != nil {
			t.Fatal(err)
		}
		return
	}
	st, manager, vm, proxy := newAutoForwardFixture(t)
	vm.Network.AutoForward = false
	port := freeLoopbackPort(t)
	endpoint := forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: port}
	vm.Network.Forwards = []forward.Decl{endpoint}
	if err := st.SaveVM(vm); err != nil {
		t.Fatal(err)
	}
	proxy.install(forward.Address(endpoint.Bind, port))
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	entered := make(chan string, 1)
	proxy.mu.Lock()
	proxy.delayAction, proxy.release, proxy.entered = "unexpose", release, entered
	proxy.mu.Unlock()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestReconcileRemovalAfterCLIExit$")
	cmd.Env = append(os.Environ(), "VOOM_RECOVERY_CHILD=1", "VOOM_RECOVERY_PORT="+strconv.Itoa(port))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	var id string
	select {
	case id = <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("removal did not reach gvproxy")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	saved, err := st.LoadVM(vm.Name)
	if err != nil || len(saved.Network.Forwards) != 0 {
		t.Fatalf("removal intent was not durable: %+v %v", saved, err)
	}
	if reserved, err := st.PortReserved("tcp", "0.0.0.0", port); err != nil || !reserved {
		t.Fatalf("CLI exit released reservation: %t %v", reserved, err)
	}
	rows, err := manager.ForwardRows(context.Background(), []string{vm.Name})
	if err != nil || len(rows) != 2 || rows[1].Kind != "reservation" || !strings.Contains(rows[1].Reason, "forward reconcile demo") {
		t.Fatalf("pending removal status: %+v %v", rows, err)
	}
	releaseOnce.Do(func() { close(release) })
	if err := process.WaitFor(func() bool {
		proxy.mu.Lock()
		defer proxy.mu.Unlock()
		return proxy.finished[id]
	}, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := New(st).ReconcileForwards(context.Background(), vm.Name); err != nil {
		t.Fatal(err)
	}
	if reserved, err := st.PortReserved("tcp", "0.0.0.0", port); err != nil || reserved {
		t.Fatalf("completed removal retained reservation: %t %v", reserved, err)
	}
	if _, unexposes := proxy.counts(); unexposes != 1 {
		t.Fatalf("removal was repeated: %d", unexposes)
	}
	if !manager.IsRunning(vm) {
		t.Fatal("recovery stopped the VM")
	}
}

func TestReconcileRetriesOnlyAgainstOriginalProcess(t *testing.T) {
	for _, mode := range []string{"acknowledged", "completed", "unsent", "not-delivered", "missing", "replaced", "pending", "unsupported", "retired", "retired-before-delivery", "missing-auto-intent"} {
		t.Run(mode, func(t *testing.T) {
			st, m, vm, proxy := newAutoForwardFixture(t)
			vm.Network.AutoForward = false
			endpoint := forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: freeLoopbackPort(t), GuestPort: 8080}
			vm.Network.Forwards = []forward.Decl{endpoint}
			if err := st.SaveVM(vm); err != nil {
				t.Fatal(err)
			}
			rt := st.Runtime(vm)
			fingerprint, err := process.RecordFingerprint(rt.GVProxyProcessRecord())
			if err != nil {
				t.Fatal(err)
			}
			ids, err := gvproxy.AllocateForwardIDs(context.Background(), rt.NetworkSock(), 2)
			if err != nil {
				t.Fatal(err)
			}
			request := forward.MutationRequest{ID: ids[0], Kind: forward.ManualExpose, Endpoint: endpoint, GuestTargetIP: m.GuestTargetIP(vm), Result: forward.UnknownMutation()}
			switch mode {
			case "unsent":
				request.Result = forward.UnsubmittedMutation()
			case "missing-auto-intent":
				request.Kind = request.Kind.Automatic()
			case "acknowledged", "completed", "missing":
				request.Result, err = gvproxy.ChangeForward(context.Background(), rt.NetworkSock(), request.Action(), mutationEndpoint(request), request.ID)
				if err != nil {
					t.Fatal(err)
				}
				if mode != "completed" {
					if err := gvproxy.AcknowledgeForward(context.Background(), rt.NetworkSock(), request.ID); err != nil {
						t.Fatal(err)
					}
					request.Result = request.Result.Acknowledged()
				}
				if mode == "missing" {
					proxy.mu.Lock()
					delete(proxy.installed, forwardSocket{endpoint.Protocol, forward.Address(endpoint.Bind, endpoint.HostPort)})
					proxy.mu.Unlock()
				}
			case "retired", "retired-before-delivery":
				action := "expose"
				if mode == "retired-before-delivery" {
					action = "unexpose"
				}
				if _, err := gvproxy.ChangeForward(context.Background(), rt.NetworkSock(), action, mutationEndpoint(request), ids[1]); err != nil {
					t.Fatal(err)
				}
				if err := gvproxy.AcknowledgeForward(context.Background(), rt.NetworkSock(), ids[1]); err != nil {
					t.Fatal(err)
				}
			case "pending":
				startPendingForwardRequest(t, proxy, rt.NetworkSock(), request)
			case "unsupported":
				proxy.capabilityStatus = 404
			}
			journal := forward.MutationJournal{Version: 1, Process: fingerprint, Requests: []forward.MutationRequest{request}}
			if err := state.WriteJSONAtomic(rt.ForwardMutation(), journal); err != nil {
				t.Fatal(err)
			}
			if mode == "replaced" {
				writeProcessRecord(t, rt.GVProxyProcessRecord(), fakeNamedVMProcess(t, "gvproxy"))
			}
			before, _ := proxy.counts()
			_, err = New(st).ReconcileForwards(context.Background(), vm.Name)
			blocked := mode == "replaced" || mode == "pending" || mode == "unsupported" || mode == "missing-auto-intent"
			if blocked {
				if !errors.Is(err, errForwardRecoveryRequired) || !m.forwardMutationPending(vm) {
					t.Fatalf("unproven request released journal: %v", err)
				}
			} else if err != nil || m.forwardMutationPending(vm) {
				t.Fatalf("recovery did not complete: %v", err)
			}
			want := before
			if mode == "unsent" || mode == "not-delivered" || mode == "missing" || mode == "retired-before-delivery" {
				want++
			}
			if exposes, _ := proxy.counts(); exposes != want {
				t.Fatalf("executed mutations=%d, want %d", exposes, want)
			}
			if !m.IsRunning(vm) {
				t.Fatal("recovery stopped the VM")
			}
		})
	}
}

func startPendingForwardRequest(t *testing.T, proxy *fakeAutoForwardGVProxy, sock string, request forward.MutationRequest) {
	t.Helper()
	release, entered := make(chan struct{}), make(chan string, 1)
	proxy.mu.Lock()
	proxy.delayAction, proxy.release, proxy.entered = request.Action(), release, entered
	proxy.mu.Unlock()
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := gvproxy.ChangeForward(ctx, sock, request.Action(), mutationEndpoint(request), request.ID)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach forwarding handler")
	}
	cancel()
	if err := <-done; !errors.Is(err, gvproxy.ErrForwardOutcomeUnknown) {
		t.Fatalf("pending submission: %v", err)
	}
}

func TestReconcileDelayedAutomaticRemoval(t *testing.T) {
	st, manager, vm, proxy := newAutoForwardFixture(t)
	port := freeLoopbackPort(t)
	row := RuntimeAutoForward{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: port, GuestTargetIP: manager.GuestTargetIP(vm), Status: forward.ReservationActive, GVProxyPID: manager.runtimeGVProxyPID(vm)}
	if err := manager.saveRuntimeAutoForwards(vm, []RuntimeAutoForward{row}); err != nil {
		t.Fatal(err)
	}
	proxy.install(forward.Address(row.Bind, port))
	writeAutoForwardReport(t, st, vm, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now()})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	entered := make(chan string, 1)
	proxy.mu.Lock()
	proxy.delayAction, proxy.release, proxy.entered = "unexpose", release, entered
	proxy.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := manager.ReconcileForwards(ctx, vm.Name); result <- err }()
	var id string
	select {
	case id = <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("automatic removal did not arrive")
	}
	cancel()
	if err := <-result; !errors.Is(err, gvproxy.ErrForwardOutcomeUnknown) {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	if err := process.WaitFor(func() bool {
		proxy.mu.Lock()
		defer proxy.mu.Unlock()
		return proxy.finished[id]
	}, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := New(st).ReconcileForwards(context.Background(), vm.Name); err != nil {
		t.Fatal(err)
	}
	if rows, err := manager.ReadRuntimeAutoForwards(vm); err != nil || len(rows) != 0 {
		t.Fatalf("removed automatic forward retained: %+v %v", rows, err)
	}
	if reserved, err := st.PortReserved("tcp", "0.0.0.0", port); err != nil || reserved {
		t.Fatalf("removed automatic reservation: %t %v", reserved, err)
	}
	if _, unexposes := proxy.counts(); unexposes != 1 {
		t.Fatalf("automatic removal repeated: %d", unexposes)
	}
}

func TestReconcileRepairsMissingAutomaticListener(t *testing.T) {
	st, manager, vm, proxy := newAutoForwardFixture(t)
	port := freeLoopbackPort(t)
	row := RuntimeAutoForward{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: port, GuestTargetIP: manager.GuestTargetIP(vm), Status: forward.ReservationActive, GVProxyPID: manager.runtimeGVProxyPID(vm)}
	if err := manager.saveRuntimeAutoForwards(vm, []RuntimeAutoForward{row}); err != nil {
		t.Fatal(err)
	}
	writeAutoForwardReport(t, st, vm, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now(), Listeners: []GuestListener{{Proto: "tcp", Addr: "0.0.0.0", Port: port}}})
	for range 2 {
		rows, err := New(st).ReconcileForwards(context.Background(), vm.Name)
		if err != nil || len(rows) != 2 || !rows[1].Installed {
			t.Fatalf("automatic listener was not restored: %+v %v", rows, err)
		}
	}
	if exposes, _ := proxy.counts(); exposes != 1 {
		t.Fatalf("reconciliation repeated installation: %d", exposes)
	}
}

func TestRemoveForwardReportsUnsavedIntent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires filesystem permission enforcement")
	}
	st, manager, vm, proxy := newAutoForwardFixture(t)
	vm.Network.AutoForward = false
	port := freeLoopbackPort(t)
	endpoint := forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: port}
	vm.Network.Forwards = []forward.Decl{endpoint}
	if err := st.SaveVM(vm); err != nil {
		t.Fatal(err)
	}
	proxy.install(forward.Address(endpoint.Bind, port))
	if err := os.Chmod(st.VMDir(vm.ID), 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(st.VMDir(vm.ID), 0700) })
	if err := manager.RemoveForward(context.Background(), vm.Name, port, endpoint.Bind, endpoint.Protocol); err == nil {
		t.Fatal("unsaved removal reported success")
	}
	if saved, err := st.LoadVM(vm.Name); err != nil || len(saved.Network.Forwards) != 1 {
		t.Fatalf("failed removal changed intent: %+v %v", saved, err)
	}
	if _, unexposes := proxy.counts(); unexposes != 0 {
		t.Fatalf("removal preceded persistence: %d", unexposes)
	}
	if reserved, err := st.PortReserved("tcp", "0.0.0.0", port); err != nil || !reserved {
		t.Fatalf("failed removal released reservation: %t %v", reserved, err)
	}
}

func TestUDPProbeFailureDoesNotCorruptJournal(t *testing.T) {
	for _, status := range []int{404, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			st, manager, vm, proxy := newAutoForwardFixture(t)
			proxy.capabilityStatus, proxy.acknowledgeStatus = status, 500
			port := freeLoopbackPort(t)
			if _, err := manager.AddForward(context.Background(), vm.Name, port, port, "127.0.0.1", false, "udp"); err == nil {
				t.Fatal("capability probe failure reported success")
			}
			if _, err := forward.ReadMutationJournal(st.Runtime(vm).ForwardMutation()); err != nil {
				t.Fatalf("probe error corrupted journal: %v", err)
			}
			if manager.forwardMutationPending(vm) {
				t.Fatal("an unsubmitted request blocked further forwarding changes")
			}
			if exposes, unexposes := proxy.counts(); exposes != 0 || unexposes != 0 {
				t.Fatalf("failed capability probe sent a mutation: %d %d", exposes, unexposes)
			}
		})
	}
}

func TestRuntimeWatcherOwnsRecoveryScheduleAndDiagnostics(t *testing.T) {
	st, manager, vm, proxy := newAutoForwardFixture(t)
	recorder := &recordingEmitter{}
	manager.events = recorder
	fingerprint, err := process.RecordFingerprint(st.Runtime(vm).GVProxyProcessRecord())
	if err != nil {
		t.Fatal(err)
	}
	ids, err := gvproxy.AllocateForwardIDs(context.Background(), st.Runtime(vm).NetworkSock(), 1)
	if err != nil {
		t.Fatal(err)
	}
	request := forward.MutationRequest{ID: ids[0], Kind: forward.ManualExpose, GuestTargetIP: manager.GuestTargetIP(vm), Endpoint: forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: freeLoopbackPort(t), GuestPort: 8080}, Result: forward.UnknownMutation()}
	if err := state.WriteJSONAtomic(st.Runtime(vm).ForwardMutation(), forward.MutationJournal{Version: 1, Process: fingerprint, Requests: []forward.MutationRequest{request}}); err != nil {
		t.Fatal(err)
	}
	startPendingForwardRequest(t, proxy, st.Runtime(vm).NetworkSock(), request)
	fingerprint, err = process.RecordFingerprint(st.Runtime(vm).VMProcessRecord())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runtimeDone := make(chan error, 1)
	go func() { runtimeDone <- manager.WatchRuntime(ctx, vm.ID, fingerprint) }()
	if err := <-runtimeDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	proxy.mu.Lock()
	submissions := proxy.submissions
	proxy.mu.Unlock()
	if submissions < 2 || submissions > 3 {
		t.Fatalf("recovery attempts: %d", submissions)
	}
	if len(recorder.events) != 1 || recorder.events[0].Action != "recovery" {
		t.Fatalf("duplicate recovery diagnostics: %+v", recorder.events)
	}
}
