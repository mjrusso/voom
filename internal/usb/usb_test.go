package usb

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRoute(t *testing.T) {
	tests := []struct {
		input      string
		controller string
		protocol   int
		port       string
	}{
		{input: "usb-0000:00:14.0@2-3", controller: "0000:00:14.0", protocol: 2, port: "3"},
		{input: "usb-platform-xhci@3-03.004", controller: "platform-xhci", protocol: 3, port: "3.4"},
		{input: " usb-controller_1@1-1.2.3 ", controller: "controller_1", protocol: 1, port: "1.2.3"},
	}
	for _, tt := range tests {
		route, err := ParseRoute(tt.input)
		if err != nil {
			t.Fatalf("ParseRoute(%q): %v", tt.input, err)
		}
		if route.Controller != tt.controller || route.Protocol != tt.protocol || route.Port != tt.port {
			t.Fatalf("ParseRoute(%q) = %#v; want %q, %d, %q", tt.input, route, tt.controller, tt.protocol, tt.port)
		}
	}
	for _, input := range []string{"", "1-3.2", "usb-", "usb-controller-1", "usb-controller@0-1", "usb-controller@10-1", "usb-controller@2-0", "usb-controller@2-a", "usb-controller@2-1..2", "usb-controller/unsafe@2-1", "usb-controller@2-1.2.3.4.5.6.7.8"} {
		if _, err := ParseRoute(input); err == nil {
			t.Errorf("ParseRoute(%q) succeeded", input)
		}
	}
}

func TestDiscoverUsesStableControllerRoute(t *testing.T) {
	sysfs := t.TempDir()
	devRoot := t.TempDir()
	writeFakeRootHub(t, sysfs, 1, "0000:00:14.0", "2.00")
	writeFakeRootHub(t, sysfs, 2, "platform-xhci", "3.20")
	writeFakeDevice(t, sysfs, devRoot, "2-3.1", "2", "7", "303A", "1001", "Espressif", "USB JTAG/serial debug unit", "ABC123", "00")
	writeFakeDevice(t, sysfs, devRoot, "1-2", "1", "4", "1366", "1055", "SEGGER", "J-Link", "000123", "00")
	writeFakeDevice(t, sysfs, devRoot, "1-4", "1", "5", "1234", "5678", "Hub vendor", "Hub", "", "09")
	if err := os.Mkdir(filepath.Join(sysfs, "1-2:1.0"), 0o755); err != nil {
		t.Fatal(err)
	}

	devices := testInventory(t, sysfs, devRoot).Devices()
	if len(devices) != 2 {
		t.Fatalf("devices = %#v", devices)
	}
	if devices[0].Location() != "usb-0000:00:14.0@2-2" || devices[0].Bus != 1 || devices[0].VendorID != "1366" || devices[0].Product != "J-Link" || !devices[0].Accessible {
		t.Fatalf("first device = %#v", devices[0])
	}
	if devices[1].Location() != "usb-platform-xhci@3-3.1" || devices[1].Bus != 2 || devices[1].VendorID != "303a" || devices[1].Serial != "ABC123" || !devices[1].Accessible {
		t.Fatalf("second device = %#v", devices[1])
	}
}

func TestResolveUsesCurrentBusNumber(t *testing.T) {
	sysfs := t.TempDir()
	devRoot := t.TempDir()
	writeFakeRootHub(t, sysfs, 7, "0000:00:14.0", "2.00")
	decl := Decl{Name: "board", Route: Route{Controller: "0000:00:14.0", Protocol: 2, Port: "3.2"}}
	binding, err := testInventory(t, sysfs, devRoot).Resolve(decl)
	if err != nil {
		t.Fatalf("resolve = %#v, %v", binding, err)
	}
	if binding.Name != "board" || binding.Bus != 7 || binding.Port != "3.2" {
		t.Fatalf("binding = %#v", binding)
	}

	writeFakeRootHub(t, sysfs, 8, "0000:00:14.0", "3.20")
	if binding, err := testInventory(t, sysfs, devRoot).Resolve(decl); err != nil || binding.Bus != 7 {
		t.Fatalf("protocol-specific resolve = %#v, %v", binding, err)
	}

	writeFakeRootHub(t, sysfs, 9, "0000:00:14.0", "2.10")
	if _, err := testInventory(t, sysfs, devRoot).Resolve(decl); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous resolve error = %v", err)
	}
}

func TestReadDeviceMissingAndHub(t *testing.T) {
	sysfs := t.TempDir()
	devRoot := t.TempDir()
	writeFakeRootHub(t, sysfs, 1, "controller", "2.00")
	missing := Decl{Name: "missing", Route: Route{Controller: "controller", Protocol: 2, Port: "2"}}
	if _, connected, err := testInventory(t, sysfs, devRoot).Inspect(missing); err != nil || connected {
		t.Fatalf("missing device = %t, %v", connected, err)
	}
	writeFakeDevice(t, sysfs, devRoot, "1-4", "1", "5", "1234", "5678", "", "Hub", "", "09")
	hub := Decl{Name: "hub", Route: Route{Controller: "controller", Protocol: 2, Port: "4"}}
	if _, _, err := testInventory(t, sysfs, devRoot).Inspect(hub); err == nil {
		t.Fatal("hub lookup succeeded")
	}
}

func TestNewDecl(t *testing.T) {
	decl, err := NewDecl("board1", "usb-0000:00:14.0@2-3.4")
	if err != nil {
		t.Fatal(err)
	}
	if decl.Name != "board1" || decl.Controller != "0000:00:14.0" || decl.Protocol != 2 || decl.Port != "3.4" || decl.Location() != "usb-0000:00:14.0@2-3.4" {
		t.Fatalf("decl = %#v", decl)
	}
	for _, name := range []string{"", "-board", "bad name"} {
		if _, err := NewDecl(name, "usb-controller@2-1"); err == nil {
			t.Errorf("NewDecl(%q) succeeded", name)
		}
	}
	if _, err := NewDecl(strings.Repeat("a", qemuDeviceIDMaxLen-len(qemuDeviceIDPrefix)+1), "usb-controller@2-1"); err == nil {
		t.Error("NewDecl accepted a name that exceeds QEMU's device ID limit")
	}
}

func TestDeclDoesNotPersistRuntimeBus(t *testing.T) {
	data, err := json.Marshal(Decl{Name: "board", Route: Route{Controller: "0000:00:14.0", Protocol: 2, Port: "3.2"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), `{"name":"board","controller":"0000:00:14.0","protocol":2,"port":"3.2"}`; got != want {
		t.Fatalf("declaration JSON = %s", data)
	}
	var decoded Decl
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.Route != (Route{Controller: "0000:00:14.0", Protocol: 2, Port: "3.2"}) {
		t.Fatalf("decoded declaration = %#v, %v", decoded, err)
	}
}

func TestDeviceJSONIncludesCanonicalLocation(t *testing.T) {
	data, err := json.Marshal(Device{Route: Route{Controller: "controller", Protocol: 2, Port: "3.2"}, Bus: 7})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"location":"usb-controller@2-3.2"`, `"controller":"controller"`, `"protocol":2`, `"port":"3.2"`, `"bus":7`} {
		if !strings.Contains(string(data), field) {
			t.Fatalf("device JSON %s does not contain %s", data, field)
		}
	}
}

func TestDeclValidateRejectsUntrustedFields(t *testing.T) {
	for _, decl := range []Decl{
		{Name: "bad,name", Route: Route{Controller: "controller", Protocol: 2, Port: "2"}},
		{Name: "board", Route: Route{Controller: "", Protocol: 2, Port: "2"}},
		{Name: "board", Route: Route{Controller: "bad/controller", Protocol: 2, Port: "2"}},
		{Name: "board", Route: Route{Controller: "controller", Protocol: 0, Port: "2"}},
		{Name: "board", Route: Route{Controller: "controller", Protocol: 2, Port: "02"}},
		{Name: "board", Route: Route{Controller: "controller", Protocol: 2, Port: "2\nquit"}},
	} {
		if err := decl.Validate(); err == nil {
			t.Errorf("Validate(%#v) succeeded", decl)
		}
	}
}

func TestValidateAssignments(t *testing.T) {
	valid := Decl{Name: "board", Route: Route{Controller: "controller", Protocol: 2, Port: "2"}}
	if err := ValidateAssignments([]Decl{valid}); err != nil {
		t.Fatal(err)
	}
	for _, assignments := range [][]Decl{
		{valid, {Name: "board", Route: Route{Controller: "controller", Protocol: 2, Port: "3"}}},
		{valid, {Name: "probe", Route: Route{Controller: "controller", Protocol: 2, Port: "2"}}},
	} {
		if err := ValidateAssignments(assignments); err == nil {
			t.Fatalf("ValidateAssignments(%#v) succeeded", assignments)
		}
	}
}

func TestInspectAndResolveStableRoute(t *testing.T) {
	sysfs := t.TempDir()
	devRoot := t.TempDir()
	decl := Decl{Name: "board", Route: Route{Controller: "0000:00:14.0", Protocol: 2, Port: "2"}}
	inventory := testInventory(t, sysfs, devRoot)
	if _, connected, err := inventory.Inspect(decl); err == nil || connected || !strings.Contains(err.Error(), "controller route") {
		t.Fatalf("missing controller = %t, %v", connected, err)
	}
	if err := inventory.CheckAccess(decl); err == nil || !strings.Contains(err.Error(), "controller route") {
		t.Fatalf("missing controller access error = %v", err)
	}
	if _, err := inventory.Resolve(decl); err == nil {
		t.Fatal("resolved missing controller")
	}
	writeFakeRootHub(t, sysfs, 7, "0000:00:14.0", "2.00")
	inventory = testInventory(t, sysfs, devRoot)
	if _, connected, err := inventory.Inspect(decl); err != nil || connected {
		t.Fatalf("disconnected device = %t, %v", connected, err)
	}
	if err := inventory.CheckAccess(decl); err != nil {
		t.Fatalf("disconnected device access error = %v", err)
	}
	if binding, err := inventory.Resolve(decl); err != nil || binding.Bus != 7 || binding.Port != "2" {
		t.Fatalf("disconnected binding = %#v, %v", binding, err)
	}
	writeFakeDevice(t, sysfs, devRoot, "7-2", "7", "4", "303a", "1001", "Espressif", "USB JTAG/serial debug unit", "ABC123", "00")
	inventory = testInventory(t, sysfs, devRoot)
	if device, connected, err := inventory.Inspect(decl); err != nil || !connected || !device.Accessible || device.Bus != 7 {
		t.Fatalf("accessible device = %#v, %t, %v", device, connected, err)
	}
	if binding, err := inventory.Resolve(decl); err != nil || binding.Bus != 7 {
		t.Fatalf("accessible binding = %#v, %v", binding, err)
	}
	if err := os.Remove(filepath.Join(devRoot, "007", "004")); err != nil {
		t.Fatal(err)
	}
	inventory = testInventory(t, sysfs, devRoot)
	device, connected, err := inventory.Inspect(decl)
	if err != nil || !connected || device.Accessible {
		t.Fatalf("inaccessible device = %#v, %t, %v", device, connected, err)
	}
	if _, err := inventory.Resolve(decl); err == nil || !strings.Contains(err.Error(), "not accessible") {
		t.Fatalf("inaccessible binding error = %v", err)
	}
}

func TestInventoryKeepsScannedTopology(t *testing.T) {
	sysfs := t.TempDir()
	devRoot := t.TempDir()
	writeFakeRootHub(t, sysfs, 1, "controller", "2.00")
	writeFakeDevice(t, sysfs, devRoot, "1-2", "1", "4", "1234", "5678", "vendor", "device", "serial", "00")
	decl := Decl{Name: "device", Route: Route{Controller: "controller", Protocol: 2, Port: "2"}}
	inventory := testInventory(t, sysfs, devRoot)

	if err := os.RemoveAll(filepath.Join(sysfs, "1-2")); err != nil {
		t.Fatal(err)
	}
	if device, connected, err := inventory.Inspect(decl); err != nil || !connected || device.Product != "device" {
		t.Fatalf("scanned device = %#v, %t, %v", device, connected, err)
	}
	if _, connected, err := testInventory(t, sysfs, devRoot).Inspect(decl); err != nil || connected {
		t.Fatalf("rescanned device = %t, %v", connected, err)
	}
}

func testInventory(t *testing.T, sysfs, devRoot string) *Inventory {
	t.Helper()
	inventory, err := scanInventory(sysfs, devRoot)
	if err != nil {
		t.Fatal(err)
	}
	return inventory
}

func writeFakeRootHub(t *testing.T, sysfs string, bus int, controller, version string) {
	t.Helper()
	dir := filepath.Join(sysfs, fmt.Sprintf("usb%d", bus))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"serial": controller, "version": version, "busnum": fmt.Sprint(bus)} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func writeFakeDevice(t *testing.T, sysfs, devRoot, location, bus, address, vendor, product, manufacturer, productName, serial, class string) {
	t.Helper()
	dir := filepath.Join(sysfs, location)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"busnum":       bus,
		"devnum":       address,
		"idVendor":     vendor,
		"idProduct":    product,
		"manufacturer": manufacturer,
		"product":      productName,
		"serial":       serial,
		"bDeviceClass": class,
	}
	for name, value := range files {
		if value == "" && (name == "manufacturer" || name == "serial") {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	busNumber := 0
	deviceNumber := 0
	if _, err := fmt.Sscan(bus, &busNumber); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Sscan(address, &deviceNumber); err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(devRoot, fmt.Sprintf("%03d", busNumber), fmt.Sprintf("%03d", deviceNumber))
	if err := os.MkdirAll(filepath.Dir(node), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(node, nil, 0o666); err != nil {
		t.Fatal(err)
	}
}
