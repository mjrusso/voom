package vm

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/process"
)

func TestForwardMutationWithoutCommitRequiresPersistedReservation(t *testing.T) {
	for _, action := range []forwardAction{forwardExpose, forwardUnexpose} {
		t.Run(action.String(), func(t *testing.T) {
			st, m, rec, proxy := newAutoForwardFixture(t)
			endpoint := forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: freeLoopbackPort(t), GuestPort: 8080}
			rec.Network.Forwards = append(rec.Network.Forwards, endpoint)
			unlock, err := st.TryLockVM(rec.ID)
			if err != nil || unlock == nil {
				t.Fatalf("VM lock: %v", err)
			}
			defer unlock()
			err = m.executeForwardMutation(rec, forwardMutation{
				operations: []forwardOperation{{action: action, endpoint: endpoint, guestTargetIP: m.GuestTargetIP(rec)}},
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
			if err := m.executeForwardMutation(rec, forwardMutation{
				operations: []forwardOperation{{action: action, endpoint: endpoint, guestTargetIP: m.GuestTargetIP(rec)}},
			}); err != nil {
				t.Fatalf("reserved mutation: %v", err)
			}
			if _, err := os.Stat(st.Runtime(rec).ForwardMutation()); !os.IsNotExist(err) {
				t.Fatalf("completed mutation retained its journal: %v", err)
			}
		})
	}
}

func TestForwardCommitFailureRequiresRecoveryWithoutUnknownBackendOutcome(t *testing.T) {
	st, m, rec, proxy := newAutoForwardFixture(t)
	gateway := fakeNamedVMProcess(t, "gvproxy")
	writeProcessRecord(t, st.Runtime(rec).GVProxyProcessRecord(), gateway)
	endpoint := forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: freeLoopbackPort(t), GuestPort: 8080}
	rec.Network.Forwards = []forward.Decl{endpoint}
	if err := st.SaveVM(rec); err != nil {
		t.Fatal(err)
	}
	unlock, err := st.TryLockVM(rec.ID)
	if err != nil || unlock == nil {
		t.Fatalf("VM lock: %v", err)
	}
	saveErr := errors.New("save failed")
	err = m.executeForwardMutation(rec, forwardMutation{
		operations: []forwardOperation{{endpoint: endpoint, guestTargetIP: m.GuestTargetIP(rec)}},
		commit:     func([]forwardOutcome) error { return saveErr },
	})
	unlock()
	if !errors.Is(err, saveErr) || !errors.Is(err, errForwardRecoveryRequired) || errors.Is(err, gvproxy.ErrForwardOutcomeUnknown) {
		t.Fatalf("commit failure: %v", err)
	}
	if !m.IsRunning(rec) || !m.forwardMutationPending(rec) {
		t.Fatal("executor changed runtime or released its journal")
	}
	if exposes, unexposes := proxy.counts(); exposes != 1 || unexposes != 0 {
		t.Fatalf("backend mutations: expose=%d unexpose=%d", exposes, unexposes)
	}
	if _, err := m.ReconcileAutoForwards(context.Background(), rec.Name); !errors.Is(err, errForwardRecoveryRequired) || errors.Is(err, gvproxy.ErrForwardOutcomeUnknown) {
		t.Fatalf("live recovery: %v", err)
	}
	if m.IsRunning(rec) || m.forwardMutationPending(rec) || process.HasRecord(st.Runtime(rec).GVProxyProcessRecord()) {
		t.Fatal("live recovery left runtime or journal")
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
	commitErr := errors.New("commit interrupted")
	err = m.executeForwardMutation(rec, forwardMutation{
		rejection: forwardSkipRejected,
		operations: []forwardOperation{{endpoint: forward.Decl{
			Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: 8080,
		}}},
		commit: func(outcomes []forwardOutcome) error {
			if len(outcomes) != 1 || !errors.Is(outcomes[0].err, errForwardPortReserved) {
				t.Errorf("rejected outcomes: %#v", outcomes)
			}
			if m.forwardMutationPending(rec) {
				t.Error("rejected batch published a journal before commit")
			}
			return commitErr
		},
	})
	unlock()
	if !errors.Is(err, commitErr) || errors.Is(err, errForwardRecoveryRequired) {
		t.Fatalf("rejected batch commit: %v", err)
	}
	if exposes, unexposes := proxy.counts(); exposes != 0 || unexposes != 0 {
		t.Fatalf("rejected batch contacted gvproxy: %d %d", exposes, unexposes)
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
