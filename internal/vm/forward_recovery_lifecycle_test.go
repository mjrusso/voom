package vm

import (
	"context"
	"errors"
	"testing"
)

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
