package vm

import (
	"context"
	"testing"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
)

func TestRecoveryPreservesOtherTransportOnSharedPort(t *testing.T) {
	for _, removedProtocol := range []string{"tcp", "udp"} {
		t.Run(removedProtocol, func(t *testing.T) {
			st, m, vm, proxy := newAutoForwardFixture(t)
			port := freeLoopbackPort(t)
			for _, protocol := range []string{"tcp", "udp"} {
				if _, err := m.AddForward(context.Background(), vm.Name, 8080, port, "127.0.0.1", false, protocol); err != nil {
					t.Fatal(err)
				}
			}
			installed, err := gvproxy.Forwards(context.Background(), st.Runtime(vm).NetworkSock())
			if err != nil || len(installed) != 3 {
				t.Fatalf("shared-port transports were lost: %+v, %v", installed, err)
			}
			proxy.dropNextResponses(1)
			if err := m.RemoveForward(context.Background(), vm.Name, port, "127.0.0.1", removedProtocol); err != nil {
				t.Fatal(err)
			}
			installed, err = gvproxy.Forwards(context.Background(), st.Runtime(vm).NetworkSock())
			if err != nil || len(installed) != 2 {
				t.Fatalf("removal changed another transport: %+v, %v", installed, err)
			}
			for endpoint := range installed {
				if endpoint.Local == forward.Address("127.0.0.1", port) && endpoint.Protocol == removedProtocol {
					t.Fatalf("removed transport remains installed: %+v", endpoint)
				}
			}
		})
	}
}
