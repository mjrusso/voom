package vm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

func TestRejectedRetryPreservesEarlierUncertainty(t *testing.T) {
	st, m, vm, proxy := newAutoForwardFixture(t)
	vm.Network.AutoForward = false
	if err := st.SaveVM(vm); err != nil {
		t.Fatal(err)
	}
	proxy.dropNextResponses(-1)
	port := freeLoopbackPort(t)
	if _, err := m.AddForward(context.Background(), vm.Name, 8080, port, "127.0.0.1", false, "udp"); !errors.Is(err, errForwardRecoveryRequired) {
		t.Fatalf("lost response: %v", err)
	}
	proxy.mu.Lock()
	proxy.capabilities = map[string]int{gvproxy.ForwardRecoveryCapability: 1}
	proxy.mu.Unlock()
	if err := m.recoverPendingForwardMutation(context.Background(), vm); !errors.Is(err, errForwardRecoveryRequired) {
		t.Fatalf("rejected retry settled the earlier request: %v", err)
	}
	journal, err := forward.ReadMutationJournal(st.Runtime(vm).ForwardMutation())
	if err != nil || journal == nil || len(journal.Requests) != 1 || journal.Requests[0].Result.Phase != forward.MutationUnknown {
		t.Fatalf("rejected retry lost uncertainty: %+v %v", journal, err)
	}
	if reserved, err := st.PortReserved("udp", "0.0.0.0", port); err != nil || !reserved {
		t.Fatalf("rejected retry released reservation: %t %v", reserved, err)
	}
	proxy.mu.Lock()
	proxy.capabilities = nil
	clear(proxy.dropResponses)
	proxy.mu.Unlock()
	if _, err := New(st).ReconcileForwards(context.Background(), vm.Name); err != nil {
		t.Fatal(err)
	}
	if m.forwardMutationPending(vm) {
		t.Fatal("confirmed retry retained the journal")
	}
	if exposes, _ := proxy.counts(); exposes != 1 {
		t.Fatalf("retry repeated expose: %d", exposes)
	}
}

func TestConfirmedRejectionDoesNotBlockForwardingChanges(t *testing.T) {
	for _, path := range []string{"execution", "recovery", "lost-response"} {
		t.Run(path, func(t *testing.T) {
			st, m, vm, proxy := newAutoForwardFixture(t)
			vm.Network.AutoForward = false
			firstPort, secondPort := freeLoopbackPort(t), freeLoopbackPort(t)
			for secondPort == firstPort {
				secondPort = freeLoopbackPort(t)
			}
			first := forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: firstPort, GuestPort: firstPort}
			second := forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: secondPort, GuestPort: secondPort}
			vm.Network.Forwards = []forward.Decl{first, second}
			if err := st.SaveVM(vm); err != nil {
				t.Fatal(err)
			}
			proxy.exposeStatuses = map[string]int{forward.Address(second.Bind, second.HostPort): 409}
			if path == "recovery" {
				proxy.acknowledgeStatus = 500
			}
			if path == "lost-response" {
				proxy.dropResponses = map[string]int{"2": 1}
			}
			if _, err := m.ReconcileForwards(context.Background(), vm.Name); err == nil {
				t.Fatal("rejected declaration reported success")
			}
			if m.forwardMutationPending(vm) != (path == "recovery") {
				t.Fatal("journal state does not match acknowledgement status")
			}
			proxy.mu.Lock()
			proxy.acknowledgeStatus = 0
			proxy.mu.Unlock()
			if err := m.RemoveForward(context.Background(), vm.Name, second.HostPort, second.Bind, second.Protocol); err != nil {
				t.Fatalf("cannot remove rejected declaration: %v", err)
			}
			rows, err := m.ForwardRows(context.Background(), []string{vm.Name})
			if err != nil || len(rows) != 2 || !rows[1].Installed {
				t.Fatalf("successful declaration was lost: %+v, %v", rows, err)
			}
			if m.forwardMutationPending(vm) {
				t.Fatal("confirmed rejection left forwarding changes blocked")
			}
			if exposes, unexposes := proxy.counts(); exposes != 2 || unexposes != 1 {
				t.Fatalf("completion was resubmitted: expose=%d, unexpose=%d", exposes, unexposes)
			}
		})
	}
}

func TestRecoveryVerifiesCompletedBatchOnce(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		snapshotDelay, ackDelay, budget time.Duration
		attempts                        int
	}{
		{"snapshot", 100 * time.Millisecond, 0, 600 * time.Millisecond, 5},
		{"acknowledgement", 0, 100 * time.Millisecond, 350 * time.Millisecond, 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, m, vm, proxy := newAutoForwardFixture(t)
			vm.Network.AutoForward = false
			rt := st.Runtime(vm)
			fingerprint, err := process.RecordFingerprint(rt.GVProxyProcessRecord())
			if err != nil {
				t.Fatal(err)
			}
			journal := forward.MutationJournal{Version: 1, Process: fingerprint}
			ids, err := gvproxy.AllocateForwardIDs(context.Background(), rt.NetworkSock(), 8)
			if err != nil {
				t.Fatal(err)
			}
			for i := range 8 {
				port := 18080 + i
				endpoint := forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: port}
				vm.Network.Forwards = append(vm.Network.Forwards, endpoint)
				request := forward.MutationRequest{ID: ids[i], Kind: forward.ManualExpose, Endpoint: endpoint, GuestTargetIP: m.GuestTargetIP(vm)}
				request.Result, err = gvproxy.ChangeForward(context.Background(), rt.NetworkSock(), request.Action(), mutationEndpoint(request), request.ID)
				if err != nil {
					t.Fatal(err)
				}
				journal.Requests = append(journal.Requests, request)
			}
			if err := st.SaveVM(vm); err != nil {
				t.Fatal(err)
			}
			if err := state.WriteJSONAtomic(rt.ForwardMutation(), journal); err != nil {
				t.Fatal(err)
			}
			proxy.mu.Lock()
			proxy.snapshotDelay, proxy.acknowledgeDelay = tc.snapshotDelay, tc.ackDelay
			proxy.mu.Unlock()
			attempts := 0
			for range tc.attempts {
				attempts++
				ctx, cancel := context.WithTimeout(context.Background(), tc.budget)
				err = m.recoverPendingForwardMutation(ctx, vm)
				cancel()
				if err == nil {
					break
				}
			}
			if err != nil || m.forwardMutationPending(vm) {
				t.Fatalf("completed prefix exhausted every recovery deadline: %v", err)
			}
			proxy.mu.Lock()
			snapshots := proxy.snapshots
			proxy.mu.Unlock()
			if snapshots < 1 || snapshots > attempts {
				t.Fatalf("batch used %d forwarding snapshots in %d attempts", snapshots, attempts)
			}
		})
	}
}

func TestConfirmedRemovalRejectionRetainsReservationWithoutBlockingChanges(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual", true: "automatic"}[automatic], func(t *testing.T) {
			st, m, vm, proxy := newAutoForwardFixture(t)
			port := freeLoopbackPort(t)
			endpoint := forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: port}
			proxy.install(forward.Address(endpoint.Bind, port))
			proxy.unexposeStatus = 400
			if automatic {
				row := RuntimeAutoForward{Protocol: endpoint.Protocol, Bind: endpoint.Bind, HostPort: port, GuestPort: port, GuestTargetIP: m.GuestTargetIP(vm), Status: forward.ReservationActive, GVProxyPID: m.runtimeGVProxyPID(vm)}
				if err := m.saveRuntimeAutoForwards(vm, []RuntimeAutoForward{row}); err != nil {
					t.Fatal(err)
				}
				writeAutoForwardReport(t, st, vm, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now()})
			} else {
				vm.Network.Forwards = []forward.Decl{endpoint}
				if err := st.SaveVM(vm); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			if automatic {
				_, err = m.ReconcileAutoForwards(context.Background(), vm.Name)
			} else {
				err = m.RemoveForward(context.Background(), vm.Name, port, endpoint.Bind, endpoint.Protocol)
			}
			if err == nil {
				t.Fatal("rejected removal reported success")
			}
			if m.forwardMutationPending(vm) {
				t.Fatal("confirmed removal rejection left changes blocked")
			}
			if reserved, err := st.PortReserved(endpoint.Protocol, endpoint.Bind, port); err != nil || !reserved {
				t.Fatalf("live listener lost its reservation: %t, %v", reserved, err)
			}
			if _, present := proxy.remote(forward.Address(endpoint.Bind, port)); !present {
				t.Fatal("rejected removal changed the listener")
			}
			other := freeLoopbackPort(t)
			for other == port {
				other = freeLoopbackPort(t)
			}
			if _, err := m.AddForward(context.Background(), vm.Name, other, other, "127.0.0.1", false, "tcp"); err != nil {
				t.Fatalf("unrelated forwarding change remained blocked: %v", err)
			}
		})
	}
}

func TestLostExposeRejectionIsReturnedToCurrentAdd(t *testing.T) {
	st, m, vm, proxy := newAutoForwardFixture(t)
	proxy.exposeStatus = 409
	proxy.dropNextResponses(1)
	port := freeLoopbackPort(t)
	if _, err := m.AddForward(context.Background(), vm.Name, port, port, "127.0.0.1", false, "tcp"); err == nil {
		t.Fatal("recovered rejection reported success")
	}
	saved, err := st.LoadVM(vm.Name)
	if err != nil || len(saved.Network.Forwards) != 0 {
		t.Fatalf("rejected forward remained declared: %+v, %v", saved, err)
	}
	if m.forwardMutationPending(vm) {
		t.Fatal("confirmed rejection retained journal")
	}
	if reserved, err := st.PortReserved("tcp", "127.0.0.1", port); err != nil || reserved {
		t.Fatalf("rejected add retained reservation: %t, %v", reserved, err)
	}
	if exposes, _ := proxy.counts(); exposes != 1 {
		t.Fatalf("recovery repeated rejected mutation: %d", exposes)
	}
}
