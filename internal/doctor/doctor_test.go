package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	if err := os.WriteFile(rt.AutoForwardProcessRecord(), []byte("invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rows := []forward.RuntimeAuto{{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 18080, GuestPort: 8080, GuestTargetIP: state.DefaultGuestTargetIP("qemu"), Status: "active"}}
	if err := forward.WriteRuntimeState(rt.AutoForwardsJSON(), rows); err != nil {
		t.Fatal(err)
	}
	checks := StateDiagnostics(st)
	want := map[string]bool{
		"stale-process-record-scratch-auto-forward.process.json": false,
		"stale-auto-forward-state-scratch":                       false,
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

func TestDoctorChecksVfkitExtensions(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("vfkit host check requires Darwin")
	}
	root := t.TempDir()
	for _, kind := range []string{"CONFIG", "STATE", "CACHE", "RUNTIME"} {
		t.Setenv("VOOM_"+kind+"_DIR", filepath.Join(root, kind))
	}
	path := filepath.Join(root, "vfkit")
	t.Setenv("VOOM_VFKIT", path)
	for _, tc := range []struct {
		capabilities string
		ok           bool
	}{
		{`{"gui-lifecycle-v1":1,"virtiofs-readonly-v1":1}`, true},
		{`{"gui-lifecycle-v1":1}`, false},
	} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '"+tc.capabilities+"'\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, check := range Run(false) {
			if check.Name == "vfkit" {
				found = true
				if check.OK != tc.ok || !strings.Contains(check.Message, path) {
					t.Fatalf("wrong vfkit diagnostic: %+v", check)
				}
			}
		}
		if !found {
			t.Fatal("missing vfkit compatibility diagnostic")
		}
	}
}
