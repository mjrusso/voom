package cli

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mjrusso/voom/internal/pci"
	"github.com/mjrusso/voom/internal/state"
)

func TestPCICommands(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("PCI passthrough is Linux-only")
	}
	dir := t.TempDir()
	root := copyFixtureState(t, dir)
	t.Setenv("VOOM_STATE_DIR", root)
	t.Setenv("VOOM_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("VOOM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("VOOM_RUNTIME_DIR", filepath.Join(dir, "runtime"))

	st, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	vmRec, err := st.LoadVM("scratch")
	if err != nil {
		t.Fatal(err)
	}
	if vmRec.Arch != "x86_64-linux" {
		t.Skipf("fixture VM is %s", vmRec.Arch)
	}
	// The slot does not exist on the test host, so every inspection reports it.
	vmRec.PCIDevices = []pci.Decl{{Name: "gpu", Slot: pci.Slot{Bus: 0xfe, Device: 0x1f}, Functions: []pci.Function{{Number: 0, Vendor: "10de", Device: "1e84"}}}}
	if err := st.SaveVM(vmRec); err != nil {
		t.Fatal(err)
	}
	if err := runCmdErr("gpu", "add", "scratch", "other", "0000:fe:1f"); err == nil || !strings.Contains(err.Error(), "no PCI device at 0000:fe:1f") {
		t.Fatalf("missing slot error = %v", err)
	}
	if out := runCmd(t, "pci", "list", "scratch"); !strings.Contains(out, "gpu  0000:fe:1f.0  10de:1e84") {
		t.Fatalf("PCI list output:\n%s", out)
	}
	if out := runCmd(t, "list"); !strings.Contains(out, "pci=1") {
		t.Fatalf("list output:\n%s", out)
	}
	out := runCmd(t, "info", "scratch")
	if !strings.Contains(out, "pci: 1") || !strings.Contains(out, "pci-device: gpu 0000:fe:1f.0 unavailable") || !strings.Contains(out, "pci-error: gpu: no PCI device at 0000:fe:1f") {
		t.Fatalf("info output:\n%s", out)
	}
	runCmd(t, "pci", "remove", "scratch", "gpu")
	out = runCmd(t, "--output", "json", "pci", "ls", "scratch")
	var decls []pci.Decl
	if err := json.Unmarshal([]byte(out), &decls); err != nil || len(decls) != 0 {
		t.Fatalf("PCI list JSON: %v %s", err, out)
	}
}
