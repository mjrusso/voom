package gvproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGatewayCapabilityRequiresExactVersion(t *testing.T) {
	for _, data := range []string{`{}`, `{"guest-isolation-v1":1,"gateway-forward-v1":0}`, `{"guest-isolation-v1":1,"gateway-forward-v1":2}`, `unknown`, `{"guest-isolation-v1":1,"gateway-forward-v1":true}`} {
		if err := checkCapabilities([]byte(data), GuestIsolationCapability, GatewayCapability); err == nil {
			t.Errorf("accepted capability %s", data)
		}
	}
	if err := checkCapabilities([]byte(`{"guest-isolation-v1":1,"gateway-forward-v1":1}`), GuestIsolationCapability, GatewayCapability); err != nil {
		t.Fatal(err)
	}
}

func TestGuestIsolationCapabilityRequiresExactVersion(t *testing.T) {
	for _, data := range []string{`{}`, `{"guest-isolation-v1":0}`, `{"guest-isolation-v1":2}`, `unknown`, `{"guest-isolation-v1":true}`} {
		if err := checkCapabilities([]byte(data), GuestIsolationCapability); err == nil {
			t.Errorf("accepted capability %s", data)
		}
	}
	if err := checkCapabilities([]byte(`{"guest-isolation-v1":1}`), GuestIsolationCapability); err != nil {
		t.Fatal(err)
	}
}

func TestResolveCompatibleReturnsCheckedExecutable(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "gvproxy")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '{\"guest-isolation-v1\":1}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VOOM_GVPROXY", executable)
	got, err := ResolveCompatible(context.Background(), GuestIsolationCapability)
	if err != nil {
		t.Fatal(err)
	}
	if got != executable {
		t.Fatalf("executable = %s, want %s", got, executable)
	}
}

func TestGatewayArgumentPreservesSocketPath(t *testing.T) {
	route := GatewayRoute{Local: "192.168.127.1:3128", Target: "/tmp/a=b 'quote' $variable.sock"}
	var got GatewayRoute
	if err := json.Unmarshal([]byte(GatewayArgument(route)), &got); err != nil {
		t.Fatal(err)
	}
	if got != route {
		t.Fatalf("argument changed path: %+v", got)
	}
}

func TestExposeResponseClassification(t *testing.T) {
	dir, err := os.MkdirTemp("", "gateway-http-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	socket := filepath.Join(dir, "api.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	statuses := make(chan int, 1)
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		status := <-statuses
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// An incomplete error body must not erase a definite rejection status.
		_, _ = fmt.Fprintf(buf, "HTTP/1.1 %d rejected\r\nContent-Length: 20\r\nConnection: close\r\n\r\nx", status)
		_ = buf.Flush()
	})}
	defer func() { _ = server.Close() }()
	go func() { _ = server.Serve(listener) }()
	for _, status := range []int{400, 408, 409, 500} {
		statuses <- status
		err := ExposeGateway(context.Background(), socket, GatewayRoute{Local: "192.168.127.1:3128", Target: "/tmp/backend.sock"})
		if err == nil || GatewayRejected(err) != (status != 500) {
			t.Fatalf("HTTP %d: %v", status, err)
		}
	}
}
