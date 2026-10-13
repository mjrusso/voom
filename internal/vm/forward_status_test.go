package vm

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"testing"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/state"
)

func TestForwardRowsUsesOneBackendSnapshot(t *testing.T) {
	st, _ := newTestStore(t)
	rec := &state.VMRecord{
		SchemaVersion: state.SchemaVersion,
		ID:            "status", Name: "status", Driver: "qemu",
		Network: state.VMNetwork{
			SSHBind: "127.0.0.1", SSHPort: 22,
			Forwards: []forward.Decl{
				{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 40001, GuestPort: 8080},
				{Protocol: "udp", Bind: "127.0.0.1", HostPort: 40001, GuestPort: 5353},
				{Protocol: "udp", Bind: "127.0.0.1", HostPort: 40002, GuestPort: 5353},
				{Protocol: "udp", Bind: "127.0.0.1", HostPort: 40003, GuestPort: 5353},
				{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 40004, GuestPort: 8080},
				{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 40005, GuestPort: 8080},
			},
		},
	}
	if err := os.MkdirAll(st.VMDir(rec.ID), 0755); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVM(rec); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(rec.Name, rec.ID); err != nil {
		t.Fatal(err)
	}
	rt := st.Runtime(rec)
	if err := os.MkdirAll(rt.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	var unavailable atomic.Bool
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/services/forwarder/all" {
			t.Errorf("unexpected observation request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		requests.Add(1)
		if unavailable.Load() {
			http.Error(w, "backend unavailable", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode([]gvproxy.Forward{
			{Protocol: "tcp", Local: "127.0.0.1:22", Remote: "192.168.127.3:22"},
			{Protocol: "udp", Local: "127.0.0.1:40001", Remote: "192.168.127.3:5353"},
			{Protocol: "udp", Local: "127.0.0.1:40002", Remote: "192.168.127.3:9999"},
			{Protocol: "tcp", Local: "127.0.0.1:40003", Remote: "192.168.127.3:5353"},
			{Protocol: "tcp", Local: "127.0.0.1:40004", Remote: "192.168.127.3:8080"},
			{Protocol: "tcp", Local: "127.0.0.1:40005", Remote: "192.168.127.3:9999"},
		})
	})}
	ln, err := net.Listen("unix", rt.NetworkSock())
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	m := New(st)
	rows, err := m.ForwardRows(context.Background(), []string{rec.Name})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Installed {
			t.Fatalf("stopped VM forward: %#v", row)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("stopped VM queried gvproxy")
	}
	driver := fakeNamedVMProcess(t, "qemu-system-test")
	writeProcessRecord(t, rt.VMProcessRecord(), driver)
	rows, err = m.ForwardRows(context.Background(), []string{rec.Name})
	if err != nil || len(rows) != 7 {
		t.Fatalf("forward status: %#v %v", rows, err)
	}
	for i, want := range []bool{true, false, true, false, false, true, false} {
		if rows[i].Installed != want {
			t.Fatalf("forward %d: %#v, want installed=%t", i, rows[i], want)
		}
		if want && rows[i].Status != "running" {
			t.Fatalf("installed declared forward changed status: %#v", rows[i])
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("snapshot requests: %d", requests.Load())
	}
	unavailable.Store(true)
	rows, err = m.ForwardRows(context.Background(), []string{rec.Name})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Installed || row.Status != "unavailable" || row.Reason == "" {
			t.Fatalf("unavailable backend: %#v", row)
		}
	}
	unavailable.Store(false)
	requests.Store(0)
	rec.Network.Forwards = rec.Network.Forwards[4:5]
	if err := st.SaveVM(rec); err != nil {
		t.Fatal(err)
	}
	rows, err = m.ForwardRows(context.Background(), []string{rec.Name})
	if err != nil || len(rows) != 2 || !rows[1].Installed {
		t.Fatalf("TCP-only status: %#v %v", rows, err)
	}
	if requests.Load() != 1 {
		t.Fatalf("TCP-only snapshot requests: %d", requests.Load())
	}
}
