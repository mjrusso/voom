package vm

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/process"
)

func TestFailedCloseRetainsReservationDespiteAbsentSnapshot(t *testing.T) {
	for _, protocol := range []string{"tcp", "udp"} {
		t.Run(protocol, func(t *testing.T) {
			st, m, vm, proxy := newAutoForwardFixture(t)
			vm.Network.AutoForward = false
			if err := st.SaveVM(vm); err != nil {
				t.Fatal(err)
			}
			endpoint, err := m.AddForward(context.Background(), vm.Name, 8080, freeLoopbackPort(t), "127.0.0.1", false, protocol)
			if err != nil {
				t.Fatal(err)
			}
			proxy.failUnexpose(500)
			snapshot := []gvproxy.Forward{{Protocol: "tcp", Local: forward.Address(vm.Network.SSHBind, vm.Network.SSHPort), Remote: forward.Address(m.GuestTargetIP(vm), 22)}}
			proxy.mu.Lock()
			proxy.snapshotOverride = &snapshot
			proxy.mu.Unlock()
			if err := m.RemoveForward(context.Background(), vm.Name, endpoint.HostPort, endpoint.Bind, protocol); err == nil || !m.forwardMutationPending(vm) {
				t.Fatalf("failed close accepted an absent snapshot: pending=%t error=%v", m.forwardMutationPending(vm), err)
			}
			if reserved, err := st.PortReserved(protocol, "0.0.0.0", endpoint.HostPort); err != nil || !reserved {
				t.Fatalf("failed close released reservation: %t %v", reserved, err)
			}
			proxy.mu.Lock()
			proxy.unexposeStatus, proxy.snapshotOverride = 0, nil
			proxy.mu.Unlock()
			if _, err := New(st).ReconcileForwards(context.Background(), vm.Name); err != nil {
				t.Fatal(err)
			}
			if reserved, err := st.PortReserved(protocol, "0.0.0.0", endpoint.HostPort); err != nil || reserved {
				t.Fatalf("confirmed close retained reservation: %t %v", reserved, err)
			}
		})
	}
}

func TestRejectedCloseRetryKeepsRecoveryPending(t *testing.T) {
	st, m, vm, proxy := newAutoForwardFixture(t)
	vm.Network.AutoForward = false
	if err := st.SaveVM(vm); err != nil {
		t.Fatal(err)
	}
	endpoint, err := m.AddForward(context.Background(), vm.Name, 8080, freeLoopbackPort(t), "127.0.0.1", false, "tcp")
	if err != nil {
		t.Fatal(err)
	}
	release, entered := make(chan struct{}), make(chan string, 1)
	var once sync.Once
	snapshot := []gvproxy.Forward{{Protocol: "tcp", Local: forward.Address(vm.Network.SSHBind, vm.Network.SSHPort), Remote: forward.Address(m.GuestTargetIP(vm), 22)}}
	proxy.mu.Lock()
	proxy.unexposeStatus, proxy.delayAction, proxy.release, proxy.entered, proxy.snapshotOverride = 500, "unexpose", release, entered, &snapshot
	proxy.mu.Unlock()
	done, finished := make(chan error, 1), make(chan struct{})
	go func() {
		defer close(finished)
		done <- m.RemoveForward(context.Background(), vm.Name, endpoint.HostPort, endpoint.Bind, endpoint.Protocol)
	}()
	t.Cleanup(func() { once.Do(func() { close(release) }); <-finished })
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("removal did not reach gvproxy")
	}
	proxy.mu.Lock()
	proxy.unexposeStatus, proxy.delayAction = 400, ""
	proxy.mu.Unlock()
	once.Do(func() { close(release) })
	if err := <-done; !errors.Is(err, errForwardRecoveryRequired) || !m.forwardMutationPending(vm) {
		t.Fatalf("rejected retry forgot the failed close: pending=%t error=%v", m.forwardMutationPending(vm), err)
	}
	if reserved, err := st.PortReserved("tcp", "0.0.0.0", endpoint.HostPort); err != nil || !reserved {
		t.Fatalf("rejected retry released reservation: %t %v", reserved, err)
	}
	proxy.mu.Lock()
	proxy.unexposeStatus, proxy.snapshotOverride = 0, nil
	proxy.mu.Unlock()
	if _, err := New(st).ReconcileForwards(context.Background(), vm.Name); err != nil {
		t.Fatal(err)
	}
	if reserved, err := st.PortReserved("tcp", "0.0.0.0", endpoint.HostPort); err != nil || reserved {
		t.Fatalf("confirmed close retained reservation: %t %v", reserved, err)
	}
}

func TestRecoveryCommitsEntireAutomaticPlan(t *testing.T) {
	for _, status := range []int{204, 400} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			st, m, vm, proxy := newAutoForwardFixture(t)
			proxy.exposeStatus = status
			unlock, err := st.TryLockVM(vm.ID)
			if err != nil || unlock == nil {
				t.Fatalf("VM lock: %v", err)
			}
			defer unlock()
			target := m.GuestTargetIP(vm)
			retainedPort, newPort := freeLoopbackPort(t), freeLoopbackPort(t)
			for newPort == retainedPort {
				newPort = freeLoopbackPort(t)
			}
			old := []RuntimeAutoForward{
				{Protocol: "tcp", Bind: "127.0.0.1", HostPort: retainedPort, GuestPort: retainedPort, GuestTargetIP: target, Status: forward.ReservationActive, GVProxyPID: m.runtimeGVProxyPID(vm)},
				{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 18081, GuestPort: 8081, Status: forward.ReservationSkipped, Reason: "obsolete"},
			}
			if err := m.saveRuntimeAutoForwards(vm, old); err != nil {
				t.Fatal(err)
			}
			proxy.install(forward.Address("127.0.0.1", retainedPort))
			plans := []forward.AutoPlan{
				{Protocol: "tcp", Bind: "127.0.0.1", HostPort: retainedPort, GuestPort: retainedPort, GuestTargetIP: target},
				{Protocol: "tcp", Bind: "127.0.0.1", HostPort: newPort, GuestPort: newPort, GuestTargetIP: target},
				{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 10022, GuestPort: 22, GuestTargetIP: target, Reason: "guest SSH port is reserved"},
			}
			release, entered := make(chan struct{}), make(chan string, 1)
			proxy.delayAction, proxy.release, proxy.entered = "expose", release, entered
			done := make(chan error, 1)
			finished := make(chan struct{})
			var releaseOnce sync.Once
			go func() {
				defer close(finished)
				_, err := m.installAutoForwardRowsLocked(context.Background(), vm, plans, old)
				done <- err
			}()
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); <-finished })
			select {
			case <-entered:
			case err := <-done:
				t.Fatalf("installation ended before expose: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("expose request did not arrive")
			}
			path := st.Runtime(vm).AutoForwardsJSON()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			releaseOnce.Do(func() { close(release) })
			if err := <-done; !errors.Is(err, errForwardRecoveryRequired) {
				t.Fatalf("commit failure lost recovery: %v", err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := m.saveRuntimeAutoForwards(vm, old); err != nil {
				t.Fatal(err)
			}
			if err := New(st).recoverPendingForwardMutation(context.Background(), vm); err != nil {
				t.Fatal(err)
			}
			rows, err := m.ReadRuntimeAutoForwards(vm)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != len(plans) {
				t.Fatalf("recovered rows omit plan intent: %+v", rows)
			}
			for i, plan := range plans {
				if i == 1 && status == 400 {
					if rows[i].Installed() || !strings.Contains(rows[i].Reason, "gvproxy expose failed") {
						t.Fatalf("rejected forward not preserved: %+v", rows[i])
					}
					continue
				}
				if rows[i].Endpoint() != plan.Endpoint() || rows[i].Reason != plan.Reason {
					t.Fatalf("recovered rows differ from plan: %+v", rows)
				}
			}
			if m.forwardMutationPending(vm) {
				t.Fatal("recovery retained journal after commit")
			}
			proxy.mu.Lock()
			proxy.delayAction = ""
			proxy.mu.Unlock()
			ordinary, err := m.installAutoForwardRowsLocked(context.Background(), vm, plans, rows)
			if err != nil || !reflect.DeepEqual(ordinary, rows) {
				t.Fatalf("execution and recovery commits differ: %+v, %v", ordinary, err)
			}
		})
	}
}

func TestCompletedReplayRequiresForwardVerification(t *testing.T) {
	for _, kind := range []forward.MutationKind{forward.ManualExpose, forward.ManualUnexpose} {
		t.Run((forward.MutationRequest{Kind: kind}).Action(), func(t *testing.T) {
			st, m, vm, proxy := newAutoForwardFixture(t)
			endpoint := forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: freeLoopbackPort(t), GuestPort: 8080}
			vm.Network.Forwards = []forward.Decl{endpoint}
			if err := st.SaveVM(vm); err != nil {
				t.Fatal(err)
			}
			if kind == forward.ManualUnexpose {
				proxy.install(forward.Address(endpoint.Bind, endpoint.HostPort))
			}
			snapshot := []gvproxy.Forward{{Protocol: "tcp", Local: forward.Address(vm.Network.SSHBind, vm.Network.SSHPort), Remote: forward.Address(m.GuestTargetIP(vm), 22)}}
			if kind == forward.ManualUnexpose {
				snapshot = append(snapshot, gvproxy.Forward{Protocol: "tcp", Local: forward.Address(endpoint.Bind, endpoint.HostPort), Remote: forward.Address(m.GuestTargetIP(vm), endpoint.GuestPort)})
			}
			proxy.snapshotOverride = &snapshot
			_, err := m.applyForwardDeclarations(context.Background(), vm, forward.MutationJournal{Requests: []forward.MutationRequest{{Kind: kind, Endpoint: endpoint, GuestTargetIP: m.GuestTargetIP(vm)}}})
			if !errors.Is(err, errForwardRecoveryRequired) || !m.forwardMutationPending(vm) {
				t.Fatalf("unverified replay cleared journal: %v", err)
			}
		})
	}
}

func TestAcknowledgementFailureRetainsJournal(t *testing.T) {
	st, m, vm, proxy := newAutoForwardFixture(t)
	proxy.acknowledgeStatus = 500
	port := freeLoopbackPort(t)
	if _, err := m.AddForward(context.Background(), vm.Name, port, port, "127.0.0.1", false, "tcp"); err == nil || !m.forwardMutationPending(vm) {
		t.Fatalf("failed acknowledgement discarded journal: %v", err)
	}
	proxy.mu.Lock()
	proxy.acknowledgeStatus = 0
	proxy.mu.Unlock()
	if err := New(st).recoverPendingForwardMutation(context.Background(), vm); err != nil {
		t.Fatal(err)
	}
	if m.forwardMutationPending(vm) {
		t.Fatal("acknowledged completion retained journal")
	}
	if exposes, _ := proxy.counts(); exposes != 1 {
		t.Fatalf("acknowledgement retry resubmitted completion: %d", exposes)
	}
}

func TestAutomaticRemovalAllowsMissingRetainedForward(t *testing.T) {
	for _, path := range []string{"reconcile", "pending-reconcile", "pending-watcher"} {
		t.Run(path, func(t *testing.T) {
			st, m, vm, proxy := newAutoForwardFixture(t)
			removedPort, retainedPort := freeLoopbackPort(t), freeLoopbackPort(t)
			for retainedPort == removedPort {
				retainedPort = freeLoopbackPort(t)
			}
			removed := RuntimeAutoForward{Protocol: "tcp", Bind: "127.0.0.1", HostPort: removedPort, GuestPort: removedPort, GuestTargetIP: m.GuestTargetIP(vm), Status: forward.ReservationActive, GVProxyPID: m.runtimeGVProxyPID(vm)}
			retained := removed
			retained.HostPort, retained.GuestPort = retainedPort, retainedPort
			if err := m.saveRuntimeAutoForwards(vm, []RuntimeAutoForward{removed, retained}); err != nil {
				t.Fatal(err)
			}
			proxy.install(forward.Address(removed.Bind, removedPort))
			writeAutoForwardReport(t, st, vm, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now(), Listeners: []GuestListener{{Proto: "tcp", Addr: "0.0.0.0", Port: retainedPort}}})
			driver, err := process.RecordFingerprint(st.Runtime(vm).VMProcessRecord())
			if err != nil {
				t.Fatal(err)
			}
			if path != "reconcile" {
				proxy.acknowledgeStatus = 500
				if _, err := m.ReconcileForwards(context.Background(), vm.Name); err == nil || !m.forwardMutationPending(vm) {
					t.Fatalf("failed acknowledgement did not preserve journal: %v", err)
				}
				journal, err := forward.ReadMutationJournal(st.Runtime(vm).ForwardMutation())
				if err != nil || journal == nil || len(journal.Requests) != 1 || journal.Requests[0].Action() != "unexpose" || journal.Requests[0].Endpoint != removed.Endpoint() {
					t.Fatalf("unexpected pending removal: %+v, %v", journal, err)
				}
				proxy.mu.Lock()
				proxy.acknowledgeStatus = 0
				proxy.mu.Unlock()
			}
			if path == "pending-watcher" {
				outcome, err := New(st).watchAutoForwardsOnce(vm.ID)
				if err != nil || outcome == autoForwardWatchSkipped || m.forwardMutationPending(vm) {
					t.Fatalf("background recovery remained blocked: outcome=%d, error=%v", outcome, err)
				}
				outcome, err = m.watchAutoForwardsOnce(vm.ID)
				if err != nil || outcome != autoForwardWatchContinue {
					t.Fatalf("automatic watcher did not repair retained listener: %v", err)
				}
			} else if _, err := New(st).ReconcileForwards(context.Background(), vm.Name); err != nil {
				t.Fatalf("reconciliation remained blocked: %v", err)
			}
			rows, err := m.ReadRuntimeAutoForwards(vm)
			if err != nil || len(rows) != 1 || rows[0].Endpoint() != retained.Endpoint() || !rows[0].Installed() || m.forwardMutationPending(vm) {
				t.Fatalf("retained listener was not repaired: %+v, %v", rows, err)
			}
			if _, present := proxy.remote(forward.Address(removed.Bind, removedPort)); present {
				t.Fatal("removed listener remains")
			}
			if target, present := proxy.remote(forward.Address(retained.Bind, retainedPort)); !present || target != forward.Address(retained.GuestTargetIP, retainedPort) {
				t.Fatalf("retained listener is absent or has the wrong target: %s", target)
			}
			if exposes, unexposes := proxy.counts(); exposes != 1 || unexposes != 1 {
				t.Fatalf("unexpected mutations: expose=%d, unexpose=%d", exposes, unexposes)
			}
			if reserved, err := st.PortReserved("tcp", removed.Bind, removedPort); err != nil || reserved {
				t.Fatalf("removed port remains reserved: %t, %v", reserved, err)
			}
			if reserved, err := st.PortReserved("tcp", retained.Bind, retainedPort); err != nil || !reserved {
				t.Fatalf("retained port is not reserved: %t, %v", reserved, err)
			}
			if current, err := process.RecordFingerprint(st.Runtime(vm).VMProcessRecord()); err != nil || current != driver || !m.IsRunning(vm) {
				t.Fatalf("repair changed the VM runtime: %v", err)
			}
		})
	}
}
