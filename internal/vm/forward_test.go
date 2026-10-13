package vm

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/state"
)

func TestManualForwardMutationsAndCleanup(t *testing.T) {
	st, _ := newTestStore(t)
	rec := &state.VMRecord{SchemaVersion: state.SchemaVersion, ID: "forward-vm", Name: "forward-vm", Driver: "qemu", Network: state.VMNetwork{SSHBind: "127.0.0.1", SSHPort: 22}}
	if err := os.MkdirAll(st.VMDir(rec.ID), 0755); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVM(rec); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(rec.Name, rec.ID); err != nil {
		t.Fatal(err)
	}
	m := New(st)
	ctx := context.Background()
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	_ = probe.Close()
	for _, proto := range []string{"", "udp"} {
		if _, err := m.AddForward(ctx, rec.Name, 5353, port, "127.0.0.1", false, proto); err != nil {
			t.Fatal(err)
		}
	}
	for _, proto := range []string{"tcp", "udp"} {
		if _, err := m.AddForward(ctx, rec.Name, 5353, port, "0.0.0.0", false, proto); err == nil {
			t.Fatalf("%s wildcard conflict accepted", proto)
		}
	}
	reserved, err := st.PortReserved("udp", "127.0.0.1", 22)
	if err != nil || reserved {
		t.Fatalf("UDP SSH reservation: %t %v", reserved, err)
	}
	rec, err = st.LoadVM(rec.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Network.Forwards) != 2 {
		t.Fatal(rec.Network.Forwards)
	}
	rt := st.Runtime(rec)
	if err := os.MkdirAll(rt.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	installed := map[string]map[string]string{}
	seedInstalled := func() {
		for _, f := range rec.Network.Forwards {
			local := forward.Address(f.Bind, f.HostPort)
			installed[f.Protocol+"/"+local] = map[string]string{"protocol": f.Protocol, "local": local, "remote": forward.Address(m.GuestTargetIP(rec), f.GuestPort)}
		}
	}
	seedInstalled()
	reject := false
	rejectRemoval := false
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/services/gateway-forward/capabilities":
			if reject {
				_, _ = w.Write([]byte(`{}`))
			} else {
				_, _ = w.Write([]byte(`{"udp-forward-v1":1}`))
			}
		case "/services/forwarder/all":
			rows := []map[string]string{}
			for _, row := range installed {
				rows = append(rows, row)
			}
			_ = json.NewEncoder(w).Encode(rows)
		case "/services/forwarder/expose", "/services/forwarder/unexpose":
			var row map[string]string
			if err := json.NewDecoder(r.Body).Decode(&row); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			key := row["protocol"] + "/" + row["local"]
			if strings.HasSuffix(r.URL.Path, "/unexpose") {
				if rejectRemoval {
					w.WriteHeader(500)
					return
				}
				delete(installed, key)
			} else {
				installed[key] = row
			}
		default:
			w.WriteHeader(404)
		}
	})}
	ln, err := net.Listen("unix", rt.NetworkSock())
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	driver := fakeNamedVMProcess(t, "qemu-system-test")
	writeProcessRecord(t, rt.VMProcessRecord(), driver)
	rows, err := m.ForwardRows(ctx, []string{rec.Name})
	if err != nil {
		t.Fatal(err)
	}
	if !rows[2].Installed || rows[2].Protocol != "udp" {
		t.Fatal(rows)
	}
	mu.Lock()
	rejectRemoval = true
	mu.Unlock()
	if err := m.RemoveForward(ctx, rec.Name, port, "127.0.0.1", "udp"); err == nil {
		t.Fatal("failed removal reported success")
	}
	persisted, err := st.LoadVM(rec.Name)
	if err != nil || len(persisted.Network.Forwards) != 2 {
		t.Fatalf("removal lost declarations: %#v %v", persisted, err)
	}
	mu.Lock()
	rejectRemoval = false
	clear(installed)
	mu.Unlock()
	rows, err = m.ForwardRows(ctx, []string{rec.Name})
	if err != nil || rows[2].Installed {
		t.Fatalf("missing backend forward reported installed: %#v %v", rows, err)
	}
	mu.Lock()
	seedInstalled()
	mu.Unlock()
	if err := m.RemoveForward(ctx, rec.Name, port, "127.0.0.1", "udp"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(installed) != 1 || installed["tcp/"+forward.Address("127.0.0.1", port)] == nil {
		t.Error(installed)
	}
	reject = true
	mu.Unlock()
	if _, err := m.AddForward(ctx, rec.Name, 5353, port, "127.0.0.1", false, "udp"); err == nil || !strings.Contains(err.Error(), "UDP forwarding unavailable") {
		t.Fatalf("unsupported backend: %v", err)
	}
	rec, err = st.LoadVM(rec.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Network.Forwards) != 1 || rec.Network.Forwards[0].Protocol != "tcp" {
		t.Fatal(rec.Network.Forwards)
	}
	mu.Lock()
	reject = false
	mu.Unlock()
	if _, err := m.AddForward(ctx, rec.Name, 5353, port, "127.0.0.1", false, "udp"); err != nil {
		t.Fatal(err)
	}
	rec, err = st.LoadVM(rec.Name)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := m.stopRuntime(ctx, rec)
	if err == nil || stop.gatewayTerminationConfirmed {
		t.Fatalf("unrecorded gateway shutdown: %+v %v", stop, err)
	}
	mu.Lock()
	if len(installed) != 0 {
		t.Error("shutdown left forwards installed", installed)
	}
	mu.Unlock()
	rec, err = st.LoadVM(rec.Name)
	if err != nil || len(rec.Network.Forwards) != 2 {
		t.Fatalf("shutdown lost declarations: %#v %v", rec, err)
	}
}
