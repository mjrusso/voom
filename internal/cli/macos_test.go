package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mjrusso/voom/internal/doctor"
	"github.com/mjrusso/voom/internal/state"
	"github.com/mjrusso/voom/internal/vm"
)

func TestMacOSImportInspectionDiagnosticsAndAccess(t *testing.T) {
	dir := t.TempDir()
	for env, child := range map[string]string{"VOOM_STATE_DIR": "state", "VOOM_CONFIG_DIR": "config", "VOOM_CACHE_DIR": "cache", "VOOM_RUNTIME_DIR": "run"} {
		t.Setenv(env, filepath.Join(dir, child))
	}
	bundle := filepath.Join(dir, "bundle")
	if err := os.Mkdir(bundle, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"image.json", "restore.json", "toolchain.json", "disk.raw", "MachineIdentifier", "HardwareModel", "AuxiliaryStorage"} {
		data := []byte(name)
		if strings.HasSuffix(name, ".json") {
			var err error
			data, err = os.ReadFile(filepath.Join("..", "..", "testdata", "macos-bundle", name))
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(bundle, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	key, err := filepath.Abs("test keys/guest")
	if err != nil {
		t.Fatal(err)
	}
	runCmd(t, "image", "import", "macos", bundle, "--ssh-identity", "test keys/guest")
	st, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	im, err := st.LoadImage("macos")
	if err != nil {
		t.Fatal(err)
	}
	out := runCmd(t, "image", "inspect", "macos")
	for _, want := range []string{"boot: macos", "image-macos: 27.0.1 (26A434)", "baked-resources: 4 CPUs, 8192MiB", "platform-state: " + st.ImageDir(im.ID)} {
		if !strings.Contains(out, want) {
			t.Fatalf("image inspect missing %q: %s", want, out)
		}
	}
	record := &state.VMRecord{SchemaVersion: im.SchemaVersion, ID: "mac", Name: "mac", Arch: im.Arch, Driver: "vfkit", Image: state.VMImageRef{ID: im.ID, Name: im.Name}, MacOS: im.MacOS, Resources: state.VMResources{CPUs: 4, MemoryMiB: 8192}, Access: state.VMAccess{SSHUser: im.Metadata.SSHUser, SSHIdentityPath: im.Metadata.SSHIdentityPath}, Network: state.VMNetwork{SSHBind: "127.0.0.1", SSHPort: 2222}}
	record.StorageGeneration = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := os.MkdirAll(st.VMStorageDir(record), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVM(record); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(record.Name, record.ID); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"disk.raw", "MachineIdentifier", "HardwareModel", "AuxiliaryStorage"} {
		data, err := os.ReadFile(filepath.Join(st.ImageDir(im.ID), name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(st.VMStorageDir(record), name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var verification bytes.Buffer
	command := NewRootCommand()
	command.SetOut(&verification)
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"--output", "json", "guest", "verify", "mac"})
	if err := command.Execute(); err == nil {
		t.Fatal("stopped VM passed guest verification")
	}
	var report vm.GuestReport
	if err := json.Unmarshal(verification.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.VMID != record.ID || report.MachineIdentifierSHA256 != im.MacOS.IdentitySHA256 || len(report.Checks) != 2 || !report.Checks[0].OK || report.Checks[1].OK {
		t.Fatalf("missing stopped-VM evidence: %+v", report)
	}

	helper := filepath.Join(t.TempDir(), "identity-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf fresh > \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VOOM_MACOS_IDENTITY", helper)
	runCmd(t, "disk", "reset", "mac", "--image", "macos")
	record, err = st.LoadVM("mac")
	if err != nil {
		t.Fatal(err)
	}
	out = runCmd(t, "info", "mac")
	if !strings.Contains(out, "image-macos: 27.0.1 (26A434)") || !strings.Contains(out, "platform-state: "+st.VMStorageDir(record)) {
		t.Fatalf("VM inspection missing platform: %s", out)
	}
	var info vmInfo
	if err := json.Unmarshal([]byte(runCmd(t, "--output", "json", "info", "mac")), &info); err != nil {
		t.Fatal(err)
	}
	if info.PlatformStateDir != st.VMStorageDir(record) || info.VM.MacOS == nil {
		t.Fatalf("VM JSON missing platform: %+v", info)
	}
	out = runCmd(t, "ssh-config", "mac")
	if !strings.Contains(out, `IdentityFile "`+key+`"`) || !strings.Contains(out, "IdentitiesOnly yes") {
		t.Fatalf("SSH identity with spaces not preserved: %s", out)
	}
	if err := os.MkdirAll(st.CacheVMDir(record), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.LogPath(record, "vfkit"), []byte("MacOS driver log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := runCmd(t, "logs", "mac"); out != "MacOS driver log\n" {
		t.Fatalf("wrong default MacOS log: %s", out)
	}
	if err := runCmdErr("console", "mac"); err == nil || !strings.Contains(err.Error(), "no serial console") {
		t.Fatalf("MacOS serial console accepted: %v", err)
	}
	for _, check := range doctor.StateDiagnostics(st) {
		if !check.OK {
			t.Fatalf("valid MacOS state reported unhealthy: %+v", check)
		}
	}
	if err := os.Remove(filepath.Join(st.VMStorageDir(record), "AuxiliaryStorage")); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, check := range doctor.StateDiagnostics(st) {
		if check.Name == "macos-storage-mac" && strings.Contains(check.Message, "AuxiliaryStorage") {
			found = true
		}
	}
	if !found {
		t.Fatal("doctor missed missing MacOS auxiliary storage")
	}
	if err := os.WriteFile(filepath.Join(st.ImageDir(im.ID), "MachineIdentifier"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	found = false
	for _, check := range doctor.StateDiagnostics(st) {
		if check.Name == "macos-image-storage-macos" && strings.Contains(check.Message, "recorded Apple identity") {
			found = true
		}
	}
	if !found {
		t.Fatal("doctor missed changed MacOS image identity")
	}
}
