package vm

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/process"
)

func TestForwardLedgerProtocol(t *testing.T) {
	for _, implementation := range []string{"fixture", "gvproxy"} {
		t.Run(implementation, func(t *testing.T) {
			var sock string
			var proxy *fakeAutoForwardGVProxy
			if implementation == "fixture" {
				st, _, vm, fixture := newAutoForwardFixture(t)
				sock, proxy = st.Runtime(vm).NetworkSock(), fixture
			} else {
				sock = startLedgerGVProxy(t)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			ids, err := gvproxy.AllocateForwardIDs(ctx, sock, 258)
			if err != nil {
				t.Fatal(err)
			}
			endpoint := gvproxy.Forward{Protocol: "tcp", Local: forward.Address("127.0.0.1", freeLoopbackPort(t)), Remote: "192.168.127.3:8080"}
			for range 3 {
				result, err := gvproxy.ChangeForward(ctx, sock, "expose", endpoint, ids[0])
				if err != nil || result.Phase != forward.MutationCompletedSucceeded {
					t.Fatalf("duplicate submission: %+v, %v", result, err)
				}
			}
			changed := endpoint
			changed.Remote = "192.168.127.3:9090"
			assertRejected := func(id, action string, endpoint gvproxy.Forward, status int) {
				t.Helper()
				result, err := gvproxy.ChangeForward(ctx, sock, action, endpoint, id)
				var response *gvproxy.HTTPError
				if result.Phase != forward.MutationRejected || !errors.As(err, &response) || response.Status != status {
					t.Fatalf("protocol rejection: %+v, %v", result, err)
				}
			}
			assertRejected(ids[0], "expose", changed, 422)
			assertRejected(ids[0], "unexpose", endpoint, 422)
			assertRejected("259", "expose", endpoint, 400)
			if proxy != nil {
				if exposes, unexposes := proxy.counts(); exposes != 1 || unexposes != 0 {
					t.Fatalf("duplicate or changed submission executed: expose=%d, unexpose=%d", exposes, unexposes)
				}
			}
			if err := gvproxy.AcknowledgeForward(ctx, sock, ids[0]); err != nil {
				t.Fatal(err)
			}
			if _, err := gvproxy.ChangeForward(ctx, sock, "unexpose", endpoint, ids[1]); err != nil {
				t.Fatal(err)
			}
			if err := gvproxy.AcknowledgeForward(ctx, sock, ids[1]); err != nil {
				t.Fatal(err)
			}
			result, err := gvproxy.ChangeForward(ctx, sock, "expose", endpoint, ids[0])
			if result.Phase != forward.MutationRetired || !errors.Is(err, gvproxy.ErrForwardRequestRetired) {
				t.Fatalf("retired submission: %+v, %v", result, err)
			}
			installed, err := gvproxy.Forwards(ctx, sock)
			if _, present := installed[endpoint]; err != nil || present {
				t.Fatalf("retired retry recreated removed listener: %+v, %v", installed, err)
			}
			for _, id := range ids[2:] {
				if _, err := gvproxy.ChangeForward(ctx, sock, "unexpose", endpoint, id); err != nil {
					t.Fatalf("fill history: %v", err)
				}
			}
			next, err := gvproxy.AllocateForwardIDs(ctx, sock, 1)
			if err != nil {
				t.Fatal(err)
			}
			assertRejected(next[0], "expose", endpoint, 429)
			if err := gvproxy.AcknowledgeForward(ctx, sock, ids[2]); err != nil {
				t.Fatal(err)
			}
			if _, err := gvproxy.ChangeForward(ctx, sock, "unexpose", endpoint, next[0]); err != nil {
				t.Fatalf("acknowledgement did not release history capacity: %v", err)
			}
		})
	}
}

func TestForwardLedgerRejectsAcknowledgementOfPendingRequest(t *testing.T) {
	st, m, vm, proxy := newAutoForwardFixture(t)
	sock := st.Runtime(vm).NetworkSock()
	ids, err := gvproxy.AllocateForwardIDs(context.Background(), sock, 1)
	if err != nil {
		t.Fatal(err)
	}
	request := forward.MutationRequest{ID: ids[0], Kind: forward.ManualExpose, GuestTargetIP: m.GuestTargetIP(vm), Endpoint: forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: freeLoopbackPort(t), GuestPort: 8080}}
	startPendingForwardRequest(t, proxy, sock, request)
	err = gvproxy.AcknowledgeForward(context.Background(), sock, request.ID)
	var response *gvproxy.HTTPError
	if !errors.As(err, &response) || response.Status != 409 {
		t.Fatalf("pending request acknowledged: %v", err)
	}
	result, err := gvproxy.ChangeForward(context.Background(), sock, request.Action(), mutationEndpoint(request), request.ID)
	if result.Phase != forward.MutationUnknown || !errors.Is(err, gvproxy.ErrForwardOutcomeUnknown) {
		t.Fatalf("pending retry: %+v, %v", result, err)
	}
	if exposes, _ := proxy.counts(); exposes != 1 {
		t.Fatalf("pending retry executed another mutation: %d", exposes)
	}
}

func startLedgerGVProxy(t *testing.T) string {
	t.Helper()
	executable, err := exec.LookPath("gvproxy")
	if err != nil {
		t.Skip("gvproxy is not installed")
	}
	dir, err := os.MkdirTemp("", "vledger-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "control.sock")
	log, err := os.Create(filepath.Join(dir, "gvproxy.log"))
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, "-listen", "unix://"+sock, "-listen-qemu", "unix://"+filepath.Join(dir, "qemu.sock"), "-ssh-port", strconv.Itoa(freeLoopbackPort(t)))
	child.Stdout, child.Stderr = log, log
	if err := child.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait(); _ = log.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := process.WaitForContext(ctx, func() bool { return gvproxy.CheckProcess(ctx, sock, gvproxy.ForwardRecoveryCapability) == nil }, 5*time.Second); err != nil {
		body, _ := os.ReadFile(log.Name())
		t.Fatalf("gvproxy startup: %v\n%s", err, body)
	}
	return sock
}
