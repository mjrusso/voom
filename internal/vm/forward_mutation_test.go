package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/mjrusso/voom/internal/forward"
)

func TestForwardMutationWithoutCommitRequiresPersistedReservation(t *testing.T) {
	for _, kind := range []forward.MutationKind{forward.ManualExpose, forward.ManualUnexpose} {
		t.Run((forward.MutationRequest{Kind: kind}).Action(), func(t *testing.T) {
			st, m, rec, proxy := newAutoForwardFixture(t)
			endpoint := forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: freeLoopbackPort(t), GuestPort: 8080}
			rec.Network.Forwards = append(rec.Network.Forwards, endpoint)
			unlock, err := st.TryLockVM(rec.ID)
			if err != nil || unlock == nil {
				t.Fatalf("VM lock: %v", err)
			}
			defer unlock()
			_, err = m.applyForwardDeclarations(context.Background(), rec, forward.MutationJournal{
				Requests: []forward.MutationRequest{{Kind: kind, Endpoint: endpoint, GuestTargetIP: m.GuestTargetIP(rec)}},
			})
			if err == nil || errors.Is(err, errForwardRecoveryRequired) {
				t.Fatalf("unreserved mutation: %v", err)
			}
			if exposes, unexposes := proxy.counts(); exposes != 0 || unexposes != 0 {
				t.Fatalf("unreserved backend mutations: %d %d", exposes, unexposes)
			}
			if m.forwardMutationPending(rec) || !m.IsRunning(rec) {
				t.Fatal("reservation validation changed the runtime")
			}
			if err := st.SaveVM(rec); err != nil {
				t.Fatal(err)
			}
			if _, err := m.applyForwardDeclarations(context.Background(), rec, forward.MutationJournal{
				Requests: []forward.MutationRequest{{Kind: kind, Endpoint: endpoint, GuestTargetIP: m.GuestTargetIP(rec)}},
			}); err != nil {
				t.Fatalf("reserved mutation: %v", err)
			}
			if _, err := os.Stat(st.Runtime(rec).ForwardMutation()); !os.IsNotExist(err) {
				t.Fatalf("completed mutation retained its journal: %v", err)
			}
		})
	}
}

func TestRejectedForwardBatchLeavesNoJournal(t *testing.T) {
	st, m, rec, proxy := newAutoForwardFixture(t)
	port := freeLoopbackPort(t)
	other := registerForwardReservationVM(t, st)
	if _, err := m.AddForward(context.Background(), other.Name, 8080, port, "127.0.0.1", false, "tcp"); err != nil {
		t.Fatal(err)
	}
	unlock, err := st.TryLockVM(rec.ID)
	if err != nil || unlock == nil {
		t.Fatalf("VM lock: %v", err)
	}
	rows := []RuntimeAutoForward{{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: 8080, GuestTargetIP: m.GuestTargetIP(rec), Status: forward.ReservationActive}}
	_, err = m.applyForwardDeclarations(context.Background(), rec, forward.MutationJournal{
		Requests:     []forward.MutationRequest{{Kind: forward.AutomaticExpose, Endpoint: rows[0].Endpoint(), GuestTargetIP: m.GuestTargetIP(rec)}},
		AutoForwards: &rows,
	})
	unlock()
	if err != nil {
		t.Fatalf("rejected batch commit: %v", err)
	}
	if rows[0].Status != forward.ReservationActive {
		t.Fatalf("preparation mutated the caller plan: %+v", rows)
	}
	if exposes, unexposes := proxy.counts(); exposes != 0 || unexposes != 0 {
		t.Fatalf("rejected batch contacted gvproxy: %d %d", exposes, unexposes)
	}
	persisted, err := m.ReadRuntimeAutoForwards(rec)
	if err != nil || len(persisted) != 1 || persisted[0].Status != forward.ReservationSkipped || persisted[0].Reason != errForwardPortReserved.Error() {
		t.Fatalf("rejected plan not saved: %+v, %v", persisted, err)
	}
	if m.forwardMutationPending(rec) || !m.IsRunning(rec) {
		t.Fatal("rejected batch changed recovery or runtime state")
	}
	nextPort := freeLoopbackPort(t)
	for nextPort == port {
		nextPort = freeLoopbackPort(t)
	}
	if _, err := m.AddForward(context.Background(), rec.Name, 8080, nextPort, "127.0.0.1", false, "tcp"); err != nil || !m.IsRunning(rec) {
		t.Fatalf("next forwarding command stopped the VM: %v", err)
	}
}

func TestForwardFailureRetainsReservedRequest(t *testing.T) {
	st, m, vm, proxy := newAutoForwardFixture(t)
	request := forward.MutationRequest{Kind: forward.ManualExpose, Endpoint: forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: freeLoopbackPort(t), GuestPort: 8080}, GuestTargetIP: m.GuestTargetIP(vm)}
	vm.Network.Forwards = append(vm.Network.Forwards, request.Endpoint)
	if err := st.SaveVM(vm); err != nil {
		t.Fatal(err)
	}
	proxy.exposeStatus = 409
	_, err := m.applyForwardDeclarations(context.Background(), vm, forward.MutationJournal{Requests: []forward.MutationRequest{request}})
	var failure *forwardMutationFailure
	if !errors.As(err, &failure) {
		t.Fatalf("missing request failure: %v", err)
	}
	if failure.request.ID == "" || failure.request.Kind != request.Kind || failure.request.Endpoint != request.Endpoint || failure.request.GuestTargetIP != request.GuestTargetIP {
		t.Fatalf("failure lost reserved intent: %+v", failure.request)
	}
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	if !proxy.finished[failure.request.ID] {
		t.Fatalf("failure refers to an unexecuted request: %s", failure.request.ID)
	}
}

func TestManualForwardAllocatesPortsForRunningAndStoppedVMs(t *testing.T) {
	st, m, running, proxy := newAutoForwardFixture(t)
	stopped := registerForwardReservationVM(t, st)
	type result struct {
		name     string
		endpoint forward.Decl
		err      error
	}
	results := make(chan result, 2)
	for _, name := range []string{running.Name, stopped.Name} {
		go func() {
			endpoint, err := m.AddForward(context.Background(), name, 8080, 0, "127.0.0.1", true, "tcp")
			results <- result{name: name, endpoint: endpoint, err: err}
		}()
	}
	ports := make(map[int]bool)
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.endpoint.HostPort == 0 || ports[got.endpoint.HostPort] {
			t.Fatalf("allocation reused a reservation: %+v", got)
		}
		ports[got.endpoint.HostPort] = true
		saved, err := st.LoadVM(got.name)
		if err != nil {
			t.Fatal(err)
		}
		if len(saved.Network.Forwards) != 1 || saved.Network.Forwards[0] != got.endpoint {
			t.Fatalf("allocated endpoint differs from declaration: %+v, %+v", got.endpoint, saved.Network.Forwards)
		}
		if m.forwardMutationPending(saved) {
			t.Fatalf("completed allocation retained a journal for %s", saved.Name)
		}
	}
	if exposes, unexposes := proxy.counts(); exposes != 1 || unexposes != 0 {
		t.Fatalf("unexpected runtime mutations: expose=%d, unexpose=%d", exposes, unexposes)
	}
}

func TestManualForwardPreparationOwnsItsInputs(t *testing.T) {
	_, m, vm, _ := newAutoForwardFixture(t)
	intent := forward.ManualMutation{Kind: forward.ManualExpose, Endpoint: forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", GuestPort: 8080}}
	proposal := forward.MutationJournal{
		Manual:   &intent,
		Requests: []forward.MutationRequest{{Kind: intent.Kind, Endpoint: intent.Endpoint, GuestTargetIP: m.GuestTargetIP(vm)}},
	}
	before, err := json.Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := m.applyForwardDeclarations(context.Background(), vm, proposal)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(proposal)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("preparation modified its caller's intent or requests: %s, %v", after, err)
	}
	if resolved.Manual.Endpoint.HostPort == 0 || resolved.Requests[0].Endpoint != resolved.Manual.Endpoint || resolved.Requests[0].ID == "" {
		t.Fatalf("preparation did not resolve allocation and identity: %+v", resolved)
	}
}
