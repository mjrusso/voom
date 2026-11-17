package vm

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

func TestRecoveryRejectsMismatchedManualIntent(t *testing.T) {
	for _, field := range []string{"removeForward", "manual"} {
		t.Run(field, func(t *testing.T) {
			st, m, vm, proxy := newAutoForwardFixture(t)
			endpoint := forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 18080, GuestPort: 8080}
			vm.Network.Forwards = []forward.Decl{endpoint}
			if err := st.SaveVM(vm); err != nil {
				t.Fatal(err)
			}
			proxy.install(forward.Address(endpoint.Bind, endpoint.HostPort))
			ids, err := gvproxy.AllocateForwardIDs(context.Background(), st.Runtime(vm).NetworkSock(), 1)
			if err != nil {
				t.Fatal(err)
			}
			fingerprint, err := process.RecordFingerprint(st.Runtime(vm).GVProxyProcessRecord())
			if err != nil {
				t.Fatal(err)
			}
			other := endpoint
			other.HostPort++
			intent := any(endpoint)
			if field == "manual" {
				intent = map[string]any{"kind": forward.ManualUnexpose, "endpoint": endpoint}
			}
			journal := map[string]any{
				"version": 1, "process": fingerprint, field: intent,
				"requests": []forward.MutationRequest{{ID: ids[0], Kind: forward.ManualUnexpose, Endpoint: other, Result: forward.UnsubmittedMutation()}},
			}
			path := st.Runtime(vm).ForwardMutation()
			if err := state.WriteJSONAtomic(path, journal); err != nil {
				t.Fatal(err)
			}
			if complete, err := m.resolvePendingForwardMutation(context.Background(), vm); complete || err == nil {
				t.Fatalf("mismatched intent accepted: complete=%t err=%v", complete, err)
			}
			saved, err := st.LoadVM(vm.Name)
			if err != nil || len(saved.Network.Forwards) != 1 || saved.Network.Forwards[0] != endpoint {
				t.Fatalf("malformed intent changed declarations: %+v %v", saved, err)
			}
			if exposes, unexposes := proxy.counts(); exposes != 0 || unexposes != 0 {
				t.Fatalf("malformed intent reached gvproxy: %d %d", exposes, unexposes)
			}
		})
	}
}

func TestReconcileDeadlineWhileWaitingForGlobalLock(t *testing.T) {
	for _, phase := range []string{"recovery", "reserve", "plan", "commit", "finish"} {
		t.Run(phase, func(t *testing.T) {
			st, m, vm, proxy := newAutoForwardFixture(t)
			vm.Network.AutoForward = phase == "plan"
			if err := st.SaveVM(vm); err != nil {
				t.Fatal(err)
			}
			if phase == "plan" {
				writeAutoForwardReport(t, st, vm, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now().UTC()})
			}
			if phase == "recovery" {
				proxy.dropNextResponses(-1)
				if _, err := m.AddForward(context.Background(), vm.Name, 8080, freeLoopbackPort(t), "127.0.0.1", false, "tcp"); err == nil {
					t.Fatal("lost response reported success")
				}
			}
			if phase == "reserve" || phase == "finish" {
				proxy.mu.Lock()
				clear(proxy.installed)
				proxy.mu.Unlock()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			entered, backendRelease := make(chan string, 1), make(chan struct{})
			if phase == "finish" || phase == "commit" {
				proxy.delayAction, proxy.entered, proxy.release = "expose", entered, backendRelease
			}
			if phase == "commit" {
				proxy.exposeStatus = 409
			}
			locked, release, released := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() {
				if phase == "finish" || phase == "commit" {
					select {
					case <-entered:
					case <-ctx.Done():
						released <- ctx.Err()
						return
					}
				}
				released <- st.WithGlobal(func() error {
					close(locked)
					close(backendRelease)
					<-release
					return nil
				})
			}()
			done, finished := make(chan error, 1), make(chan struct{})
			t.Cleanup(func() { close(release); <-released; <-finished })
			if phase != "finish" && phase != "commit" {
				<-locked
			}
			go func() {
				defer close(finished)
				if phase == "commit" {
					_, err := m.AddForward(ctx, vm.Name, 8080, freeLoopbackPort(t), "127.0.0.1", false, "tcp")
					done <- err
				} else {
					_, err := m.ReconcileForwards(ctx, vm.Name)
					done <- err
				}
			}()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("deadline lost: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("global lock wait ignored the reconciliation deadline")
			}
			select {
			case <-locked:
			default:
				t.Fatal("command did not reach the contested global lock")
			}
			unlock, err := st.TryLockVM(vm.ID)
			if err != nil || unlock == nil {
				t.Fatalf("deadline retained VM lock: %v", err)
			}
			unlock()
		})
	}
}

func TestInvalidRecoveryContractDoesNotPrepareDeclarations(t *testing.T) {
	for _, invalid := range []string{"fingerprint", "result", "automatic-plan"} {
		t.Run(invalid, func(t *testing.T) {
			st, m, vm, proxy := newAutoForwardFixture(t)
			fingerprint, err := process.RecordFingerprint(st.Runtime(vm).GVProxyProcessRecord())
			if err != nil {
				t.Fatal(err)
			}
			endpoint := forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 18080, GuestPort: 8080}
			request := forward.MutationRequest{ID: "1", Kind: forward.ManualExpose, Endpoint: endpoint, GuestTargetIP: m.GuestTargetIP(vm), Result: forward.UnknownMutation()}
			journal := forward.MutationJournal{Version: 1, Process: fingerprint, Manual: &forward.ManualMutation{Kind: forward.ManualExpose, Endpoint: endpoint}}
			switch invalid {
			case "fingerprint":
				journal.Process = "invalid"
			case "result":
				request.Result = forward.MutationResult{Phase: forward.MutationAcknowledgedSucceeded, Status: 500}
			case "automatic-plan":
				request.Kind, journal.Manual = forward.AutomaticExpose, nil
			}
			journal.Requests = []forward.MutationRequest{request}
			path := st.Runtime(vm).ForwardMutation()
			if err := state.WriteJSONAtomic(path, journal); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(st.VMDir(vm.ID), "vm.json"))
			if err != nil {
				t.Fatal(err)
			}
			if complete, err := m.resolvePendingForwardMutation(context.Background(), vm); complete || err == nil {
				t.Fatalf("invalid recovery contract accepted: complete=%t, err=%v", complete, err)
			}
			after, err := os.ReadFile(filepath.Join(st.VMDir(vm.ID), "vm.json"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("invalid recovery changed declarations: %v", err)
			}
			if _, err := forward.ReadMutationJournal(path); err == nil {
				t.Fatal("journal boundary accepted invalid recovery contract")
			}
			if exposes, unexposes := proxy.counts(); exposes != 0 || unexposes != 0 {
				t.Fatalf("invalid recovery reached backend: %d, %d", exposes, unexposes)
			}
		})
	}
}
