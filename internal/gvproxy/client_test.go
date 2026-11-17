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
	"sync/atomic"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/forward"
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
	if _, err := ChangeForward(context.Background(), sock, "expose", Forward{Protocol: "tcp", Local: "127.0.0.1:2222", Remote: "192.168.127.2:22"}, "test-request"); err != nil {
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
	_, err := ChangeForward(context.Background(), sock, "unexpose", Forward{Protocol: "tcp", Local: "127.0.0.1:2222"}, "test-request")
	var response *HTTPError
	if !errors.As(err, &response) || response.Status != http.StatusBadRequest || response.Body != "forwarder not found" {
		t.Fatalf("unexpose error = %#v", err)
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
	}{{"rejected", 400, false}, {"conflict", 409, false}, {"capacity", 429, false}, {"server-error", 500, true}, {"lost-response", 0, true}} {
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
				_, err := ChangeForward(context.Background(), sock, action, Forward{Protocol: "tcp", Local: "127.0.0.1:18080", Remote: "192.168.127.3:8080"}, "test-request")
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
	if _, err := ChangeForward(context.Background(), sock, "expose", Forward{Protocol: "udp", Local: "127.0.0.1:15353", Remote: "192.168.127.3:5353"}, "test-request"); err == nil || errors.Is(err, ErrForwardOutcomeUnknown) {
		t.Fatalf("capability rejection: %v", err)
	}
}

func TestLostMutationResponseDoesNotTriggerClientRecovery(t *testing.T) {
	var requests atomic.Int32
	sock := newFakeGVProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/services/forwarder/expose" || r.Header.Get("X-Voom-Forward-ID") != "test-request" {
			t.Errorf("unexpected recovery request: %s", r.URL.Path)
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	result, err := ChangeForward(context.Background(), sock, "expose", Forward{Protocol: "tcp", Local: "127.0.0.1:2222", Remote: "192.168.127.3:22"}, "test-request")
	if result.Phase != forward.MutationUnknown || !errors.Is(err, ErrForwardOutcomeUnknown) || requests.Load() != 1 {
		t.Fatalf("lost response: %+v, %v, requests=%d", result, err, requests.Load())
	}
}

func TestMutationResultSeparatesProbeErrorsFromHandlerResults(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, header, action string
		probe, status                  int
		phase                          forward.MutationPhase
		resultStatus                   int
	}{
		{name: "probe 404", protocol: "udp", probe: 404, phase: forward.MutationRejected},
		{name: "probe 500", protocol: "udp", probe: 500, phase: forward.MutationRejected},
		{name: "completed 500", protocol: "tcp", status: 500, header: "completed", phase: forward.MutationCompletedFailed, resultStatus: 500},
		{name: "expose conflict", protocol: "tcp", status: 409, header: "completed", phase: forward.MutationCompletedRejected, resultStatus: 409},
		{name: "unexpose conflict", action: "unexpose", protocol: "tcp", status: 409, header: "completed", phase: forward.MutationCompletedFailed, resultStatus: 409},
		{name: "pending", protocol: "tcp", status: 202, header: "pending", phase: forward.MutationUnknown},
		{name: "retired", protocol: "tcp", status: 410, header: "retired", phase: forward.MutationRetired},
		{name: "capacity rejection", protocol: "tcp", status: 429, header: "not-submitted", phase: forward.MutationRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutations := 0
			sock := newFakeGVProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/services/gateway-forward/capabilities":
					http.Error(w, "probe failed", tc.probe)
				case "/services/forwarder/expose", "/services/forwarder/unexpose":
					mutations++
					w.Header().Set("X-Voom-Forward-Outcome", tc.header)
					w.WriteHeader(tc.status)
				default:
					http.NotFound(w, r)
				}
			}))
			action := tc.action
			if action == "" {
				action = "expose"
			}
			result, err := ChangeForward(context.Background(), sock, action, Forward{Protocol: tc.protocol, Local: "127.0.0.1:8080", Remote: "192.168.127.3:8080"}, "typed-result")
			if err == nil || result.Phase != tc.phase || result.Status != tc.resultStatus || (tc.probe != 0 && mutations != 0) {
				t.Fatalf("result=%+v, error=%v, mutations=%d", result, err, mutations)
			}
			if errors.Is(err, ErrForwardOutcomeUnknown) != (result.Phase == forward.MutationUnknown) {
				t.Fatalf("typed result and error disagree: result=%+v error=%v", result, err)
			}
			kind := forward.ManualExpose
			if action == "unexpose" {
				kind = forward.ManualUnexpose
			}
			journal := forward.MutationJournal{Version: 1, Process: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Requests: []forward.MutationRequest{{ID: "typed-result", Kind: kind, Endpoint: forward.Decl{Protocol: tc.protocol, Bind: "127.0.0.1", HostPort: 8080, GuestPort: 8080}, GuestTargetIP: "192.168.127.3", Result: result}}}
			data, marshalErr := json.Marshal(journal)
			path := filepath.Join(t.TempDir(), "journal.json")
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := forward.ReadMutationJournal(path); err != nil {
				t.Fatalf("client result cannot be persisted unchanged: %v", err)
			}
		})
	}
}
