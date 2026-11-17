package vm

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mjrusso/voom/internal/forward"
)

func TestForwardRemovalIgnoresUnrelatedMalformedState(t *testing.T) {
	for _, runtimeState := range []string{"stopped", "running"} {
		for _, malformed := range []string{"record", "journal"} {
			t.Run(runtimeState+"/"+malformed, func(t *testing.T) {
				st, m, running, proxy := newAutoForwardFixture(t)
				stopped := registerForwardReservationVM(t, st)
				target, unrelated := stopped, running
				if runtimeState == "running" {
					target, unrelated = running, stopped
				}
				endpoint := forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 18080, GuestPort: 8080}
				target.Network.Forwards = []forward.Decl{endpoint}
				if err := st.SaveVM(target); err != nil {
					t.Fatal(err)
				}
				if runtimeState == "running" {
					proxy.install(forward.Address(endpoint.Bind, endpoint.HostPort))
				}
				path := filepath.Join(st.VMDir(unrelated.ID), "vm.json")
				if malformed == "journal" {
					path = st.Runtime(unrelated).ForwardMutation()
				}
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("malformed"), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := m.AddForward(context.Background(), target.Name, 9090, freeLoopbackPort(t), endpoint.Bind, false, endpoint.Protocol); err == nil {
					t.Fatal("endpoint acquisition ignored unrelated malformed reservations")
				}
				if err := m.RemoveForward(context.Background(), target.Name, endpoint.HostPort, endpoint.Bind, endpoint.Protocol); err != nil {
					t.Fatalf("unrelated %s blocked removal from %s VM: %v", malformed, runtimeState, err)
				}
				saved, err := st.LoadVM(target.Name)
				if err != nil {
					t.Fatal(err)
				}
				if len(saved.Network.Forwards) != 0 || m.forwardMutationPending(saved) {
					t.Fatalf("removal did not complete: %+v", saved.Network.Forwards)
				}
				wantUnexposes := 0
				if runtimeState == "running" {
					wantUnexposes = 1
				}
				if exposes, unexposes := proxy.counts(); exposes != 0 || unexposes != wantUnexposes {
					t.Fatalf("unexpected backend mutations: expose=%d, unexpose=%d", exposes, unexposes)
				}
			})
		}
	}
}
