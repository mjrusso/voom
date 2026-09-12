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
	for _, data := range []string{`{}`, `{"gateway-forward-v1":0}`, `{"gateway-forward-v1":2}`, `unknown`, `{"gateway-forward-v1":true}`} {
		if err := checkGatewayCapabilities([]byte(data)); err == nil {
			t.Errorf("accepted capability %s", data)
		}
	}
	if err := checkGatewayCapabilities([]byte(`{"gateway-forward-v1":1}`)); err != nil {
		t.Fatal(err)
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
