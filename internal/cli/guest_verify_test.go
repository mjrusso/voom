package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mjrusso/voom/internal/state"
	"github.com/mjrusso/voom/internal/vm"
)

func TestGuestVerifyReportsStoppedLinuxAndRejectsInvalidTimeout(t *testing.T) {
	dir := t.TempDir()
	for env, child := range map[string]string{"VOOM_STATE_DIR": "state", "VOOM_RUNTIME_DIR": "run", "VOOM_CACHE_DIR": "cache", "VOOM_CONFIG_DIR": "config"} {
		t.Setenv(env, filepath.Join(dir, child))
	}
	st, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	record := &state.VMRecord{SchemaVersion: 1, ID: "linux", Name: "linux", Arch: "x86_64-linux", Driver: "qemu", Resources: state.VMResources{CPUs: 2, MemoryMiB: 1024}}
	if err := st.SaveVM(record); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(record.Name, record.ID); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"guest", "verify", "linux", "--output", "json"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("stopped Linux VM passed")
	}
	var report vm.GuestReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.System != record.Arch || len(report.Checks) != 1 || report.Checks[0].OK || report.Checks[0].Name != "driver-running" {
		t.Fatalf("stopped evidence: %+v", report)
	}

	if err := runCmdErr("guest", "verify", "linux", "--timeout", "0s"); err == nil || !strings.Contains(err.Error(), "positive") {
		t.Fatalf("invalid timeout: %v", err)
	}
}
