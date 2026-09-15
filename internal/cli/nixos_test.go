package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

func TestNixosSwitchPreservesConfigurationChangedWhileWaiting(t *testing.T) {
	dir := t.TempDir()
	for key, sub := range map[string]string{"VOOM_STATE_DIR": "state", "VOOM_CONFIG_DIR": "config", "VOOM_CACHE_DIR": "cache", "VOOM_RUNTIME_DIR": "runtime"} {
		t.Setenv(key, filepath.Join(dir, sub))
	}
	st, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	record := &state.VMRecord{SchemaVersion: 1, ID: "switch", Name: "switch", Driver: "qemu", Image: state.VMImageRef{ID: "image"}}
	image := &state.ImageRecord{SchemaVersion: 1, ID: "image", Name: "image", Capabilities: state.ImageCapabilities{NixosSwitch: true}}
	for _, path := range []string{st.VMDir(record.ID), st.ImageDir(image.ID), st.Runtime(record).Dir()} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SaveVM(record); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(record.Name, record.ID); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(image.ID), "image.json"), image); err != nil {
		t.Fatal(err)
	}
	driver := exec.Command("bash", "-c", `exec -a qemu-system-test bash -c 'read -r ignored'`)
	stdin, err := driver.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	if err := driver.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = driver.Process.Kill(); _ = driver.Wait() }()
	if err := process.Record(st.Runtime(record).VMProcessRecord(), driver.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if err := process.WaitFor(func() bool { _, ok := process.ValidRecord(st.Runtime(record).VMProcessRecord(), "qemu"); return ok }, time.Second); err != nil {
		t.Fatal("fake driver identity unavailable")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nixos-rebuild"), []byte("#!"+sh+"\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	local, err := st.TryLockVM(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if local == nil {
		t.Fatal("VM lock was unavailable")
	}
	release := sync.OnceFunc(local)
	defer release()
	opened := make(chan struct{})
	old := openStore
	openStore = func() (*state.Store, error) { close(opened); return st, nil }
	defer func() { openStore = old }()
	command := nixosCommand()
	command.SetArgs([]string{"switch", record.Name, "--flake", "."})
	done := make(chan error, 1)
	go func() { done <- command.Execute() }()
	<-opened
	// Let switch load the old record and wait for the held VM lock.
	time.Sleep(50 * time.Millisecond)
	record.Resources.MemoryMiB = 8192
	if err := st.SaveVM(record); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("switch did not complete")
	}
	saved, err := st.LoadVM(record.Name)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Nixos == nil || saved.Resources.MemoryMiB != record.Resources.MemoryMiB {
		t.Fatalf("switch lost concurrent configuration: %+v", saved)
	}
}
