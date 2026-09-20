package cli

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mjrusso/voom/internal/state"
	"github.com/mjrusso/voom/internal/usb"
)

func TestUSBCommands(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("USB passthrough is Linux-only")
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
	vmRec.USBDevices = []usb.Decl{{Name: "board", Route: usb.Route{Controller: "test-controller", Protocol: 2, Port: "255"}}}
	if err := st.SaveVM(vmRec); err != nil {
		t.Fatal(err)
	}
	if err := runCmdErr("usb", "add", "scratch", "other", "usb-test-controller@2-255"); err == nil || !strings.Contains(err.Error(), "already assigned") {
		t.Fatalf("duplicate USB location error = %v", err)
	}
	out := runCmd(t, "usb", "list", "scratch")
	if !strings.Contains(out, "board  usb-test-controller@2-255") {
		t.Fatalf("USB list output:\n%s", out)
	}
	out = runCmd(t, "config", "show", "scratch")
	if !strings.Contains(out, "voom usb add scratch board usb-test-controller@2-255") {
		t.Fatalf("config output:\n%s", out)
	}
	out = runCmd(t, "info", "scratch")
	if !strings.Contains(out, "usb: 1") || !strings.Contains(out, "usb-device: board usb-test-controller@2-255 host=unknown runtime=stopped") || !strings.Contains(out, "usb-host-error: board: USB controller route usb-test-controller@2-255 is not available") {
		t.Fatalf("info output:\n%s", out)
	}
	out = runCmd(t, "list")
	if !strings.Contains(out, "usb=1") {
		t.Fatalf("list output:\n%s", out)
	}
	out = runCmd(t, "--output", "json", "list")
	var rows []struct {
		Name string `json:"name"`
		USB  int    `json:"usb"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 || rows[0].Name != "scratch" || rows[0].USB != 1 {
		t.Fatalf("list JSON: %v %s", err, out)
	}
	out = runCmd(t, "--output", "json", "info", "scratch")
	var info struct {
		USBStatus []struct {
			Name     string `json:"name"`
			Location string `json:"location"`
			Host     struct {
				State string `json:"state"`
				Error string `json:"error"`
			} `json:"host"`
			Runtime struct {
				State string `json:"state"`
				Error string `json:"error"`
			} `json:"runtime"`
		} `json:"usbStatus"`
	}
	if err := json.Unmarshal([]byte(out), &info); err != nil || len(info.USBStatus) != 1 {
		t.Fatalf("info JSON: %v %s", err, out)
	}
	status := info.USBStatus[0]
	if status.Name != "board" || status.Location != "usb-test-controller@2-255" || status.Host.State != "unknown" || !strings.Contains(status.Host.Error, "controller route") || status.Runtime.State != "stopped" || status.Runtime.Error != "" {
		t.Fatalf("info USB status: %#v", status)
	}
	runCmd(t, "usb", "remove", "scratch", "board")
	out = runCmd(t, "--output", "json", "usb", "ls", "scratch")
	var devices []usb.Decl
	if err := json.Unmarshal([]byte(out), &devices); err != nil || len(devices) != 0 {
		t.Fatalf("USB list JSON: %v %s", err, out)
	}
}
