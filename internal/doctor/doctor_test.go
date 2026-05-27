package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/state"
)

func TestStateDiagnosticsReportsOrphanedVM(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("VOOM_STATE_DIR", dir)
	t.Setenv("VOOM_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("VOOM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("VOOM_RUNTIME_DIR", filepath.Join(dir, "runtime"))
	st, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	vmRec := &state.VMRecord{SchemaVersion: state.SchemaVersion, ID: "vm1", Name: "scratch"}
	if err := os.MkdirAll(st.VMDir(vmRec.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSONAtomic(filepath.Join(st.VMDir(vmRec.ID), "vm.json"), vmRec); err != nil {
		t.Fatal(err)
	}
	checks := StateDiagnostics(st)
	for _, c := range checks {
		if c.Name == "state-vm-orphan-scratch" {
			return
		}
	}
	t.Fatalf("expected orphan VM diagnostic, got %#v", checks)
}

func TestStateDiagnosticsReportsStaleAutoForwardRuntimeFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("VOOM_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("VOOM_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("VOOM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("VOOM_RUNTIME_DIR", filepath.Join(dir, "runtime"))
	st, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	vmRec := &state.VMRecord{
		SchemaVersion: state.SchemaVersion,
		ID:            "vm1",
		Name:          "scratch",
		Driver:        "qemu",
		Image:         state.VMImageRef{ID: "img1", Name: "nixos"},
		Network:       state.VMNetwork{SSHPort: 2222, SSHBind: "127.0.0.1"},
	}
	if err := os.MkdirAll(st.VMDir(vmRec.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSONAtomic(filepath.Join(st.VMDir(vmRec.ID), "vm.json"), vmRec); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(vmRec.Name, vmRec.ID); err != nil {
		t.Fatal(err)
	}
	rt := st.Runtime(vmRec)
	if err := os.MkdirAll(rt.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.AutoForwardPid(), []byte("999999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows := []forward.RuntimeAuto{{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 18080, GuestPort: 8080, GuestTargetIP: state.DefaultGuestTargetIP("qemu"), Installed: true, Status: "active"}}
	if err := forward.WriteRuntimeState(rt.AutoForwardsJSON(), rows); err != nil {
		t.Fatal(err)
	}
	checks := StateDiagnostics(st)
	want := map[string]bool{
		"stale-pidfile-scratch-auto-forward.pid": false,
		"stale-auto-forward-state-scratch":       false,
	}
	for _, c := range checks {
		if _, ok := want[c.Name]; ok {
			want[c.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("missing %s in checks: %#v", name, checks)
		}
	}
}
