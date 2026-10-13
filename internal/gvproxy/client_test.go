package gvproxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shortTempDir avoids the MacOS 104-char Unix-socket path limit that t.TempDir()
// can exceed when test names are long.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gvp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func newFakeGVProxy(t *testing.T, h http.Handler) string {
	t.Helper()
	sock := filepath.Join(shortTempDir(t), "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
	})
	return sock
}

func TestExposePostsLocalAndRemote(t *testing.T) {
	var gotPath, gotBody string
	sock := newFakeGVProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotPath = r.URL.Path
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	if err := Expose(sock, "tcp", "127.0.0.1:2222", "192.168.127.2:22"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/services/forwarder/expose" {
		t.Errorf("path = %q, want /services/forwarder/expose", gotPath)
	}
	if !strings.Contains(gotBody, `"protocol":"tcp"`) || !strings.Contains(gotBody, `"local":"127.0.0.1:2222"`) || !strings.Contains(gotBody, `"remote":"192.168.127.2:22"`) {
		t.Errorf("body = %s", gotBody)
	}
}

func TestUnexposeReportsNon2xxBody(t *testing.T) {
	sock := newFakeGVProxy(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("forwarder not found\n"))
	}))
	err := Unexpose(sock, "tcp", "127.0.0.1:2222")
	var response *HTTPError
	if !errors.As(err, &response) || response.Status != http.StatusBadRequest || response.Body != "forwarder not found" {
		t.Fatalf("Unexpose error = %#v", err)
	}
}

func TestLookupLeaseMatchesMACCaseInsensitively(t *testing.T) {
	leases := map[string]string{
		"192.168.127.2":  "5A:94:EF:E4:0C:DD",
		"192.168.127.10": "02:12:34:56:78:9A",
	}
	sock := newFakeGVProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/services/dhcp/leases" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(leases)
	}))
	ip, err := LookupLease(sock, "02:12:34:56:78:9a")
	if err != nil {
		t.Fatal(err)
	}
	if ip != "192.168.127.10" {
		t.Errorf("ip = %q, want 192.168.127.10", ip)
	}
}

func TestLookupLeaseReturnsErrorWhenMACUnknown(t *testing.T) {
	sock := newFakeGVProxy(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{}"))
	}))
	_, err := LookupLease(sock, "02:12:34:56:78:9a")
	if err == nil {
		t.Fatal("expected error for missing lease")
	}
	if !strings.Contains(err.Error(), "no DHCP lease found") {
		t.Errorf("err = %v", err)
	}
}

func TestLookupLeaseRejectsMalformedJSON(t *testing.T) {
	sock := newFakeGVProxy(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	_, err := LookupLease(sock, "02:12:34:56:78:9a")
	if err == nil {
		t.Fatal("expected JSON decode error")
	}
}

func TestExposeFailsWhenSocketMissing(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "x")
	if err := Expose(sock, "tcp", "127.0.0.1:2222", "192.168.127.2:22"); err == nil {
		t.Fatal("expected dial error")
	}
}

func TestForwardsHonorsCancellation(t *testing.T) {
	entered := make(chan struct{})
	sock := newFakeGVProxy(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := Forwards(ctx, sock); result <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("snapshot request did not arrive")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot request ignored cancellation")
	}
}

func TestForwardMutationOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		unknown bool
	}{{"rejected", 400, false}, {"conflict", 409, false}, {"server-error", 500, true}, {"lost-response", 0, true}} {
		for _, action := range []string{"expose", "unexpose"} {
			t.Run(action+"-"+tc.name, func(t *testing.T) {
				sock := newFakeGVProxy(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if tc.status == 0 {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = conn.Close()
					} else {
						http.Error(w, "failed", tc.status)
					}
				}))
				var err error
				if action == "expose" {
					err = Expose(sock, "tcp", "127.0.0.1:18080", "192.168.127.3:8080")
				} else {
					err = Unexpose(sock, "tcp", "127.0.0.1:18080")
				}
				unknown := tc.unknown || (action == "unexpose" && tc.status == http.StatusConflict)
				if err == nil || errors.Is(err, ErrForwardOutcomeUnknown) != unknown {
					t.Fatalf("outcome: %v", err)
				}
			})
		}
	}
	sock := newFakeGVProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/services/gateway-forward/capabilities" {
			t.Error("unsupported backend received mutation")
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	if err := Expose(sock, "udp", "127.0.0.1:15353", "192.168.127.3:5353"); err == nil || errors.Is(err, ErrForwardOutcomeUnknown) {
		t.Fatalf("capability rejection: %v", err)
	}
}
