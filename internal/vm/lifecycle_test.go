package vm

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mjrusso/voom/internal/state"
)

func TestStartRejectsInvalidAutoForwardConfiguration(t *testing.T) {
	st, _ := newTestStore(t)
	vm := &state.VMRecord{
		SchemaVersion: state.SchemaVersion,
		ID:            "vm-id",
		Name:          "demo",
		Network: state.VMNetwork{
			AutoForward:           true,
			AutoForwardHostOffset: -1,
		},
	}
	if err := os.MkdirAll(st.VMDir(vm.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVM(vm); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(vm.Name, vm.ID); err != nil {
		t.Fatal(err)
	}
	_, err := New(st).Start(context.Background(), io.Discard, vm.Name)
	if err == nil || !strings.Contains(err.Error(), "auto-forward offset must be a non-negative integer") {
		t.Fatalf("Start error = %v", err)
	}
}

func TestStopReportsRuntimeCleanup(t *testing.T) {
	tests := []struct {
		name   string
		create func(*testing.T, state.RuntimeLayout) string
	}{
		{
			name: "auto-forward state",
			create: func(t *testing.T, rt state.RuntimeLayout) string {
				path := rt.AutoForwardsJSON()
				if err := os.WriteFile(path, []byte("[]\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "egress manifest",
			create: func(t *testing.T, rt state.RuntimeLayout) string {
				path := rt.EgressManifest()
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "stale virtiofsd lock file",
			create: func(t *testing.T, rt state.RuntimeLayout) string {
				path := filepath.Join(rt.Dir(), "virtiofs-test.sock.pid")
				if err := os.WriteFile(path, []byte("123\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := lifecycleTestStore(t)
			vm, err := st.LoadVM("a")
			if err != nil {
				t.Fatal(err)
			}
			rt := st.Runtime(vm)
			if err := os.MkdirAll(rt.Dir(), 0o755); err != nil {
				t.Fatal(err)
			}
			path := tt.create(t, rt)
			manager := New(st)
			changed, err := manager.Stop(context.Background(), vm.Name)
			if err != nil || !changed {
				t.Fatalf("Stop changed=%t error=%v", changed, err)
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("runtime artifact remains: %v", err)
			}
			changed, err = manager.Stop(context.Background(), vm.Name)
			if err != nil || changed {
				t.Fatalf("second Stop changed=%t error=%v", changed, err)
			}
		})
	}
}

func TestStopRetainsDriverSocketWithoutProcessRecord(t *testing.T) {
	st := lifecycleTestStore(t)
	vm, err := st.LoadVM("a")
	if err != nil {
		t.Fatal(err)
	}
	rt := st.Runtime(vm)
	if err := os.MkdirAll(rt.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", rt.QEMUMonitor())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	changed, err := New(st).Stop(context.Background(), vm.Name)
	if err == nil || !changed {
		t.Fatalf("Stop changed=%t error=%v", changed, err)
	}
	if _, err := os.Lstat(rt.QEMUMonitor()); err != nil {
		t.Fatalf("unconfirmed driver socket was removed: %v", err)
	}
}

func TestRemoveRetainsVMWhenRuntimeStateIsMalformed(t *testing.T) {
	st := lifecycleTestStore(t)
	vm, err := st.LoadVM("a")
	if err != nil {
		t.Fatal(err)
	}
	rt := st.Runtime(vm)
	if err := os.MkdirAll(rt.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.AutoForwardsJSON(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := New(st).Remove(context.Background(), vm.Name); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("Remove error = %v", err)
	}
	if _, err := st.LoadVM(vm.Name); err != nil {
		t.Fatalf("VM was removed after cleanup failed: %v", err)
	}
}
