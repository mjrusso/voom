package vm

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mjrusso/voom/internal/state"
)

func egressMutationStore(t *testing.T) (*state.Store, string) {
	t.Helper()
	st := lifecycleTestStore(t)
	dir, err := os.MkdirTemp("", "voom-egress-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "proxy.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(dir, "gvproxy")
	if err := os.WriteFile(executable, []byte("#!"+sh+"\nprintf '%s\\n' '{\"guest-isolation-v1\":1,\"gateway-forward-v1\":1}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VOOM_GVPROXY", executable)
	image := &state.ImageRecord{SchemaVersion: 1, ID: "image1", Name: "image", Capabilities: state.ImageCapabilities{ControlShare: true}}
	if err := os.MkdirAll(st.ImageDir(image.ID), 0700); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(image.ID), "image.json"), image); err != nil {
		t.Fatal(err)
	}
	return st, socket
}

func TestEgressStoppedMutationsAndReservations(t *testing.T) {
	st, socket := egressMutationStore(t)
	manager := New(st)
	result, err := manager.SetEgress(context.Background(), "a", socket, "")
	if err != nil || !result.Changed || !result.Enabled {
		t.Fatalf("set: %+v %v", result, err)
	}
	first, _ := st.LoadVM("a")
	result, err = manager.SetEgress(context.Background(), "a", socket, "")
	second, _ := st.LoadVM("a")
	if err != nil || result.Changed || !first.UpdatedAt.Equal(second.UpdatedAt) {
		t.Fatalf("repeat set: %+v %v", result, err)
	}
	result, err = manager.DisableEgress(context.Background(), "a")
	if err != nil || !result.Changed || result.Enabled {
		t.Fatalf("disable: %+v %v", result, err)
	}
	if _, err = manager.SetEgress(context.Background(), "b", socket, ""); err == nil {
		t.Fatal("disabled reservation reused")
	}
	t.Setenv("VOOM_GVPROXY", "")
	t.Setenv("PATH", t.TempDir())
	result, err = manager.ClearEgress(context.Background(), "a")
	if err != nil || !result.Changed || result.Egress != nil {
		t.Fatalf("clear with missing dependency: %+v %v", result, err)
	}
	result, err = manager.ClearEgress(context.Background(), "a")
	if err != nil || result.Changed {
		t.Fatalf("repeat clear: %+v %v", result, err)
	}
}

func TestEgressConcurrentReservations(t *testing.T) {
	st, socket := egressMutationStore(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, name := range []string{"a", "b"} {
		wg.Go(func() {
			manager := New(st)
			_, err := manager.SetEgress(context.Background(), name, socket, "")
			results <- err
		})
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("%d successful duplicate reservations", successes)
	}
}
