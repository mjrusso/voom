package pci

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestParseSelector(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		err  bool
	}{
		{in: "0000:41:00", want: "0000:41:00"},
		{in: "41:00.0", want: "0000:41:00.0"},
		{in: "0001:0A:1f.7", want: "0001:0a:1f.7"},
		{in: "41:00.8", err: true},
		{in: "41:20", err: true},
		{in: "41", err: true},
		{in: "41:zz", err: true},
		{in: "41::00", err: true},
	} {
		sel, err := ParseSelector(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("ParseSelector(%q) = %v, want error", tc.in, sel)
			}
			continue
		}
		if err != nil || sel.String() != tc.want {
			t.Errorf("ParseSelector(%q) = %v, %v; want %s", tc.in, sel, err, tc.want)
		}
	}
}

func TestDeclJSONUsesCanonicalSlot(t *testing.T) {
	decl := Decl{Name: "gpu", Slot: Slot{Bus: 0x41}, Functions: []Function{{Number: 0, Vendor: "10de", Device: "1e84"}}}
	b, err := json.Marshal(decl)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"slot":"0000:41:00"`) {
		t.Fatalf("encoded decl = %s", b)
	}
	var decoded Decl
	if err := json.Unmarshal(b, &decoded); err != nil || decoded.Slot != decl.Slot {
		t.Fatalf("decoded decl = %#v, %v", decoded, err)
	}
	if err := json.Unmarshal([]byte(`{"name":"gpu","slot":"41:xx"}`), &decoded); err == nil {
		t.Fatal("decoded an invalid slot")
	}
}

func TestValidateAssignments(t *testing.T) {
	gpu := func(name string, bus int) Decl {
		return Decl{Name: name, Slot: Slot{Bus: bus}, Functions: []Function{{Number: 0, Vendor: "10de", Device: "1e84"}, {Number: 1, Vendor: "10de", Device: "10f8"}}}
	}
	nine := make([]Decl, 9)
	for i := range nine {
		nine[i] = gpu("gpu"+strconv.Itoa(i), i+1)
	}
	for _, tc := range []struct {
		name string
		decl []Decl
		err  string
	}{
		{name: "valid", decl: []Decl{gpu("gpu", 0x41), gpu("nic", 0x42)}},
		{name: "duplicate name", decl: []Decl{gpu("gpu", 0x41), gpu("gpu", 0x42)}, err: "already exists"},
		{name: "duplicate slot", decl: []Decl{gpu("gpu", 0x41), gpu("nic", 0x41)}, err: "already assigned"},
		{name: "too many", decl: nine, err: "at most 8"},
		{name: "invalid name", decl: []Decl{gpu("bad,name", 0x41)}, err: "invalid PCI device name"},
		{name: "no function 0", decl: []Decl{{Name: "gpu", Functions: []Function{{Number: 1, Vendor: "10de", Device: "10f8"}, {Number: 2, Vendor: "10de", Device: "1ad8"}}}}, err: "without function 0"},
		{name: "unsorted", decl: []Decl{{Name: "gpu", Functions: []Function{{Number: 0, Vendor: "10de", Device: "1e84"}, {Number: 0, Vendor: "10de", Device: "1e84"}}}}, err: "unsorted"},
		{name: "invalid identity", decl: []Decl{{Name: "gpu", Functions: []Function{{Number: 0, Vendor: "10DE", Device: "1e84"}}}}, err: "invalid identity"},
	} {
		err := ValidateAssignments(tc.decl)
		if tc.err == "" && err != nil || tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) {
			t.Errorf("%s: ValidateAssignments error = %v, want %q", tc.name, err, tc.err)
		}
	}
}

func TestInspect(t *testing.T) {
	gpuSlot := Slot{Bus: 0x41}
	all := Selector{Slot: gpuSlot, Function: allFunctions}
	recorded := []Function{{Number: 0, Vendor: "10de", Device: "1e84"}}
	for _, tc := range []struct {
		name     string
		edit     func(f *fakeHost)
		sel      Selector
		recorded []Function
		err      string
	}{
		{name: "whole slot", sel: all},
		{name: "one function with other group members unbound", sel: Selector{Slot: gpuSlot, Function: 0}, edit: func(f *fakeHost) {
			f.setDriver("0000:41:00.1", "")
		}},
		{name: "recorded identity", sel: Selector{Slot: gpuSlot, Function: 0}, recorded: recorded},
		{name: "selected function on host driver", sel: all, edit: func(f *fakeHost) { f.setDriver("0000:41:00.0", "nouveau") }, err: "0000:41:00.0 is bound to nouveau"},
		{name: "selected function unbound", sel: all, edit: func(f *fakeHost) { f.setDriver("0000:41:00.2", "") }, err: "0000:41:00.2 is not bound to a driver"},
		{name: "other group member on host driver", sel: Selector{Slot: gpuSlot, Function: 0}, edit: func(f *fakeHost) {
			f.setDriver("0000:41:00.1", "snd_hda_intel")
		}, err: "also contains 0000:41:00.1, which is bound to snd_hda_intel"},
		{name: "changed identity", sel: Selector{Slot: gpuSlot, Function: 0}, recorded: []Function{{Number: 0, Vendor: "10de", Device: "2684"}}, err: "is 10de:1e84, but 10de:2684 was assigned"},
		{name: "missing function", sel: Selector{Slot: gpuSlot, Function: 5}, err: "0000:41:00.5 does not exist"},
		{name: "empty slot", sel: Selector{Slot: Slot{Bus: 0x42}, Function: allFunctions}, err: "no PCI device at 0000:42:00"},
		{name: "no IOMMU group", sel: all, edit: func(f *fakeHost) { f.remove("0000:41:00.0", "iommu_group") }, err: "has no IOMMU group"},
		{name: "conventional PCI", sel: all, edit: func(f *fakeHost) { f.remove("0000:41:00.0", "max_link_speed") }, err: "conventional PCI device"},
		{name: "bridge", sel: Selector{Slot: Slot{Bus: 0x40, Device: 1}, Function: 1}, err: "is a bridge"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeGPUHost(t)
			if tc.edit != nil {
				tc.edit(f)
			}
			var numbers []int
			if tc.sel.Function != allFunctions {
				numbers = []int{tc.sel.Function}
			}
			ins := f.host.inspect(tc.sel.Slot, numbers, tc.recorded)
			if tc.err == "" {
				if ins.Err != nil || len(ins.Groups) != 1 || ins.Groups[0] != 30 {
					t.Fatalf("inspection = %#v", ins)
				}
				return
			}
			if ins.Err == nil || !strings.Contains(ins.Err.Error(), tc.err) {
				t.Fatalf("inspection error = %v, want %q", ins.Err, tc.err)
			}
		})
	}
}

func TestInspectReportsInaccessibleGroupNode(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	f := newFakeGPUHost(t)
	if err := os.Chmod(filepath.Join(f.host.vfio, "30"), 0); err != nil {
		t.Fatal(err)
	}
	ins := f.host.inspect(Slot{Bus: 0x41}, nil, nil)
	if ins.Err == nil || !strings.Contains(ins.Err.Error(), "udev rule") {
		t.Fatalf("inspection error = %v", ins.Err)
	}
}

func TestCheckGroups(t *testing.T) {
	for _, tc := range []struct {
		name   string
		viable bool
		err    error
		want   string
	}{
		{name: "viable", viable: true},
		{name: "busy", err: syscall.EBUSY, want: "in use by another VM or process"},
		{name: "not viable", want: "not viable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := host{groupViable: func(string) (bool, error) { return tc.viable, tc.err }}
			err := h.checkGroups([]int{30})
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("checkGroups error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDiscoverSkipsBridgeSlots(t *testing.T) {
	f := newFakeGPUHost(t)
	slots, err := f.host.discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 1 || slots[0].Slot != (Slot{Bus: 0x41}) || len(slots[0].Devices) != 4 || slots[0].Err != nil {
		t.Fatalf("discovered = %#v", slots)
	}
}

type fakeHost struct {
	t    *testing.T
	host host
}

// newFakeGPUHost models a four-function GPU bound to vfio-pci in IOMMU group 30,
// behind a root port in its own group.
func newFakeGPUHost(t *testing.T) *fakeHost {
	t.Helper()
	root := t.TempDir()
	f := &fakeHost{t: t, host: host{
		devices: filepath.Join(root, "sys/bus/pci/devices"),
		groups:  filepath.Join(root, "sys/kernel/iommu_groups"),
		vfio:    filepath.Join(root, "dev/vfio"),
	}}
	f.mkdir(f.host.devices)
	f.addDevice("0000:40:01.1", "1022", "1453", "060400", "pcieport", 29)
	for i, id := range []string{"1e84", "10f8", "1ad8", "1ad9"} {
		f.addDevice("0000:41:00."+strconv.Itoa(i), "10de", id, "030000", vfioDriver, 30)
	}
	return f
}

func (f *fakeHost) addDevice(address, vendor, device, class, driver string, group int) {
	dir := filepath.Join(f.host.devices, address)
	f.write(filepath.Join(dir, "vendor"), "0x"+vendor+"\n")
	f.write(filepath.Join(dir, "device"), "0x"+device+"\n")
	f.write(filepath.Join(dir, "class"), "0x"+class+"\n")
	f.write(filepath.Join(dir, "max_link_speed"), "8.0 GT/s PCIe\n")
	f.setDriver(address, driver)
	groupDir := filepath.Join(f.host.groups, strconv.Itoa(group))
	f.mkdir(filepath.Join(groupDir, "devices"))
	f.symlink(groupDir, filepath.Join(dir, "iommu_group"))
	f.symlink(dir, filepath.Join(groupDir, "devices", address))
	f.write(filepath.Join(f.host.vfio, strconv.Itoa(group)), "")
}

func (f *fakeHost) setDriver(address, driver string) {
	link := filepath.Join(f.host.devices, address, "driver")
	_ = os.Remove(link)
	if driver != "" {
		f.symlink("../../../bus/pci/drivers/"+driver, link)
	}
}

func (f *fakeHost) remove(address, name string) {
	if err := os.Remove(filepath.Join(f.host.devices, address, name)); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeHost) mkdir(path string) {
	if err := os.MkdirAll(path, 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeHost) write(path, content string) {
	f.mkdir(filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeHost) symlink(target, link string) {
	if err := os.Symlink(target, link); err != nil {
		f.t.Fatal(err)
	}
}
