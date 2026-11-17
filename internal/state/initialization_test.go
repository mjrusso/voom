package state

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/host"
)

func TestOpenInitializesUnderGlobalLock(t *testing.T) {
	if ready := os.Getenv("VOOM_INITIALIZATION_HELPER"); ready != "" {
		if err := os.WriteFile(ready, nil, 0600); err != nil {
			t.Fatal(err)
		}
		st, err := Open()
		if err != nil {
			t.Fatal(err)
		}
		if got := st.IndexSnapshot().VMs["registered"]; got != "vm" {
			t.Fatalf("initializer lost registration: %q", got)
		}
		return
	}
	dir := t.TempDir()
	setTestEnv(t, dir)
	p := host.ResolvePaths()
	unlock, err := flockContext(context.Background(), filepath.Join(p.State, "locks", "state.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	ready := filepath.Join(dir, "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestOpenInitializesUnderGlobalLock$")
	cmd.Env = append(os.Environ(), "VOOM_INITIALIZATION_HELPER="+ready)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("initializer did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("initializer bypassed global lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	st := &Store{paths: p, index: Index{SchemaVersion: SchemaVersion, VMs: map[string]string{}, Images: map[string]string{}}}
	if err := st.RegisterVMLocked("registered", "vm"); err != nil {
		t.Fatal(err)
	}
	unlock()
	unlock = nil
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("initializer did not finish after unlock")
	}
	index, err := ReadIndex(p.State)
	if err != nil || index.VMs["registered"] != "vm" {
		t.Fatalf("registration overwritten: %+v, %v", index, err)
	}
}
