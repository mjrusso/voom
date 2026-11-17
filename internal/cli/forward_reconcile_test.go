package cli

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/state"
)

func TestForwardReconcileStoppedVM(t *testing.T) {
	root := t.TempDir()
	for _, kind := range []string{"CONFIG", "STATE", "CACHE", "RUNTIME"} {
		t.Setenv("VOOM_"+kind+"_DIR", filepath.Join(root, kind))
	}
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	vm := &state.VMRecord{SchemaVersion: state.SchemaVersion, ID: "dev", Name: "dev", Driver: "qemu", Network: state.VMNetwork{SSHBind: "127.0.0.1", SSHPort: 2222}}
	if err := store.SaveVM(vm); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterVM(vm.Name, vm.ID); err != nil {
		t.Fatal(err)
	}
	output := runCmd(t, "forward", "reconcile", vm.Name, "--output", "json", "--timeout", "1s")
	var rows []forward.Row
	if err := json.Unmarshal([]byte(output), &rows); err != nil || len(rows) != 1 || rows[0].Status != "stopped" || rows[0].Installed {
		t.Fatalf("stopped forwarding: %s, %v", output, err)
	}
}
