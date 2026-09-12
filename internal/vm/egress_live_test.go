package vm

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/egress"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/state"
)

func TestEgressLiveReconciliation(t *testing.T) {
	st, _ := newTestStore(t)
	record := &state.VMRecord{SchemaVersion: 1, ID: "live", Name: "live", Driver: "qemu", Image: state.VMImageRef{ID: "image1"}, Network: state.VMNetwork{Egress: &egress.Decl{Mode: egress.ModeExplicit}}}
	rt := st.Runtime(record)
	for _, dir := range []string{st.VMDir(record.ID), st.ImageDir("image1"), rt.Dir()} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	image := &state.ImageRecord{SchemaVersion: 1, ID: "image1", Name: "image", Capabilities: state.ImageCapabilities{ControlShare: true}}
	if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(image.ID), "image.json"), image); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "egress-live-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	record.Network.Egress.BackendSocket = filepath.Join(dir, "backend.sock")
	backend, err := net.Listen("unix", record.Network.Egress.BackendSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backend.Close() }()
	go func() {
		for {
			conn, err := backend.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	if err := st.SaveVM(record); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(record.Name, record.ID); err != nil {
		t.Fatal(err)
	}
	for _, helper := range []struct{ name, path string }{{"qemu-system-voom", rt.VMProcessRecord()}, {"gvproxy", rt.GVProxyProcessRecord()}} {
		cmd := fakeNamedVMProcess(t, helper.name)
		writeProcessRecord(t, helper.path, cmd)
	}
	var mu sync.Mutex
	var route *gvproxy.GatewayRoute
	exposes, unexposes := 0, 0
	failExpose := false
	rejectExpose := false
	mux := http.NewServeMux()
	mux.HandleFunc("/services/gateway-forward/capabilities", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]int{gvproxy.GatewayCapability: 1, gvproxy.GuestIsolationCapability: 1})
	})
	mux.HandleFunc("/services/gateway-forward/all", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		routes := []gvproxy.GatewayRoute{}
		if route != nil {
			routes = append(routes, *route)
		}
		_ = json.NewEncoder(w).Encode(routes)
	})
	mux.HandleFunc("/services/gateway-forward/expose", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		exposes++
		if rejectExpose {
			route = &gvproxy.GatewayRoute{Local: egress.Listener, Target: "/conflicting.sock"}
			http.Error(w, "listener conflict", http.StatusConflict)
			return
		}
		if failExpose {
			var next gvproxy.GatewayRoute
			_ = json.NewDecoder(r.Body).Decode(&next)
			route = &next
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		var next gvproxy.GatewayRoute
		if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		route = &next
		w.WriteHeader(204)
	})
	mux.HandleFunc("/services/gateway-forward/unexpose", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		unexposes++
		route = nil
		w.WriteHeader(204)
	})
	listener, err := net.Listen("unix", rt.NetworkSock())
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	defer func() { _ = server.Close() }()
	go func() { _ = server.Serve(listener) }()
	recorder := &recordingEmitter{}
	manager := New(st, WithEventEmitter(recorder))
	ctx := context.Background()
	result, err := manager.EnableEgress(ctx, "live")
	if err != nil || !result.Changed || !result.RuntimeChanged {
		t.Fatalf("enable: %+v %v", result, err)
	}
	info, err := os.Stat(rt.EgressManifest())
	if err != nil {
		t.Fatal(err)
	}
	result, err = manager.EnableEgress(ctx, "live")
	if err != nil || result.Changed || result.RuntimeChanged {
		t.Fatalf("repeat: %+v %v", result, err)
	}
	after, statErr := os.Stat(rt.EgressManifest())
	if !os.SameFile(info, after) {
		t.Fatalf("healthy enable replaced manifest: before=%+v after=%+v error=%v running=%t", info, after, statErr, manager.IsRunning(record))
	}
	if err := os.WriteFile(rt.EgressManifest(), []byte("wrong"), 0644); err != nil {
		t.Fatal(err)
	}
	result, err = manager.EnableEgress(ctx, "live")
	if err != nil || result.Changed || !result.RuntimeChanged {
		t.Fatalf("file repair: %+v %v", result, err)
	}
	mu.Lock()
	count := exposes
	mu.Unlock()
	if count != 1 {
		t.Fatalf("file repair exposed %d times", count)
	}
	if err := os.Chmod(st.VMDir(record.ID), 0500); err != nil {
		t.Fatal(err)
	}
	result, err = manager.DisableEgress(ctx, "live")
	if chmodErr := os.Chmod(st.VMDir(record.ID), 0700); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	if err == nil || result.Changed || !result.RuntimeChanged || !result.Enabled {
		t.Fatalf("failed persistence: %+v %v", result, err)
	}
	mu.Lock()
	disabled := route == nil
	mu.Unlock()
	if !disabled {
		t.Fatal("failed persistence restored access")
	}
	if len(recorder.events) == 0 || recorder.events[len(recorder.events)-1].Actor.Attributes["outcome"] != "failed" {
		t.Fatal("missing failure event")
	}
	result, err = manager.EnableEgress(ctx, "live")
	if err != nil || result.Changed || !result.RuntimeChanged {
		t.Fatalf("repair after failed persistence: %+v %v", result, err)
	}
	result, err = manager.DisableEgress(ctx, "live")
	if err != nil || !result.Changed || !result.RuntimeChanged || result.Enabled {
		t.Fatalf("disable: %+v %v", result, err)
	}
	result, err = manager.DisableEgress(ctx, "live")
	if err != nil || result.Changed || result.RuntimeChanged {
		t.Fatalf("repeat disable: %+v %v", result, err)
	}
	mu.Lock()
	rejectExpose = true
	beforeUnexposes := unexposes
	mu.Unlock()
	result, err = manager.EnableEgress(ctx, "live")
	if err == nil || result.Enabled || result.RuntimeChanged || !manager.IsRunning(record) {
		t.Fatalf("definite rejection changed runtime: %+v %v", result, err)
	}
	mu.Lock()
	preserved := route != nil && route.Target == "/conflicting.sock" && unexposes == beforeUnexposes
	rejectExpose = false
	route = nil
	mu.Unlock()
	if !preserved {
		t.Fatal("rejection rollback removed another route")
	}
	mu.Lock()
	failExpose = true
	mu.Unlock()
	result, err = manager.EnableEgress(ctx, "live")
	if err == nil || result.Enabled {
		t.Fatalf("failed expose did not restore disabled declaration: %+v %v", result, err)
	}
	if manager.IsRunning(record) {
		t.Fatal("uncertain expose did not stop the VM")
	}
	if _, err := os.Stat(rt.EgressManifest()); !os.IsNotExist(err) {
		t.Fatal("failed enable left manifest")
	}
}
