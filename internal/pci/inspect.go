package pci

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	devicesRoot = "/sys/bus/pci/devices"
	groupsRoot  = "/sys/kernel/iommu_groups"
	vfioRoot    = "/dev/vfio"

	unsafeInterruptsParameter = "/sys/module/vfio_iommu_type1/parameters/allow_unsafe_interrupts"
	vfioDriver                = "vfio-pci"
)

// Drivers the kernel accepts on the other devices of an assigned IOMMU group.
var groupSafeDrivers = map[string]bool{"": true, vfioDriver: true, "pci-stub": true, "pcieport": true}

// Device is a PCI function as currently found on the host.
type Device struct {
	Function
	Class  string `json:"class"`
	Driver string `json:"driver,omitempty"`
	Group  int    `json:"iommuGroup"`
	PCIe   bool   `json:"pcie"`
}

// Inspection describes a slot on the host and whether the selected functions
// can be assigned.
type Inspection struct {
	Slot Slot
	// Devices lists every function present in the slot.
	Devices   []Device
	Functions []Function
	Groups    []int
	Err       error
}

type host struct {
	devices     string
	groups      string
	vfio        string
	groupViable func(path string) (bool, error)
}

var defaultHost = host{devices: devicesRoot, groups: groupsRoot, vfio: vfioRoot, groupViable: vfioGroupViable}

// Inspect reports whether the functions selected by sel can be assigned. It
// reads sysfs and device-node permissions without opening VFIO groups, so it
// cannot make a concurrently starting QEMU fail to claim a group.
func Inspect(sel Selector) Inspection {
	var numbers []int
	if sel.Function != allFunctions {
		numbers = []int{sel.Function}
	}
	return defaultHost.inspect(sel.Slot, numbers, nil)
}

// InspectDecl reports whether d's functions can be assigned and still have the
// identities recorded in d.
func InspectDecl(d Decl) Inspection {
	numbers := make([]int, len(d.Functions))
	for i, f := range d.Functions {
		numbers[i] = f.Number
	}
	return defaultHost.inspect(d.Slot, numbers, d.Functions)
}

// Discover inspects every slot that contains a function other than a bridge.
func Discover() ([]Inspection, error) {
	return defaultHost.discover()
}

// CheckGroups opens each IOMMU group to confirm that the kernel reports it
// viable and that no other process holds it.
func CheckGroups(groups []int) error {
	return defaultHost.checkGroups(groups)
}

// CheckIOMMU reports an error if the kernel has created no IOMMU groups.
func CheckIOMMU() error {
	entries, err := os.ReadDir(defaultHost.groups)
	if err != nil || len(entries) == 0 {
		return errors.New("the kernel has no IOMMU groups; enable the IOMMU (AMD-Vi or VT-d) in the firmware, and on Intel hosts add intel_iommu=on to the kernel command line")
	}
	return nil
}

// CheckInterruptIsolation reports an error if VFIO is configured to assign
// devices without interrupt remapping.
func CheckInterruptIsolation() error {
	b, err := os.ReadFile(unsafeInterruptsParameter)
	if err == nil && strings.TrimSpace(string(b)) == "Y" {
		return errors.New("vfio_iommu_type1.allow_unsafe_interrupts is enabled, so a guest with an assigned device can send interrupts that the host does not isolate")
	}
	return nil
}

func (h host) discover() ([]Inspection, error) {
	entries, err := os.ReadDir(h.devices)
	if err != nil {
		return nil, fmt.Errorf("read PCI devices: %w", err)
	}
	seen := map[Slot]bool{}
	var out []Inspection
	for _, entry := range entries {
		slotText, _, ok := strings.Cut(entry.Name(), ".")
		if !ok {
			continue
		}
		slot, err := parseSlot(slotText)
		if err != nil || seen[slot] {
			continue
		}
		seen[slot] = true
		ins := h.inspect(slot, nil, nil)
		if len(ins.Devices) > 0 && allBridges(ins.Devices) {
			continue
		}
		out = append(out, ins)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slot.String() < out[j].Slot.String() })
	return out, nil
}

func (h host) inspect(slot Slot, numbers []int, recorded []Function) Inspection {
	ins := Inspection{Slot: slot}
	devices, err := h.readSlot(slot)
	if err != nil {
		ins.Err = err
		return ins
	}
	ins.Devices = devices
	if len(devices) == 0 {
		ins.Err = fmt.Errorf("no PCI device at %s", slot)
		return ins
	}
	selected := devices
	if numbers != nil {
		selected = nil
		for _, n := range numbers {
			i := slices.IndexFunc(devices, func(d Device) bool { return d.Number == n })
			if i < 0 {
				ins.Err = fmt.Errorf("PCI function %s does not exist", slot.FunctionAddress(n))
				return ins
			}
			selected = append(selected, devices[i])
		}
	}
	for _, device := range selected {
		ins.Functions = append(ins.Functions, device.Function)
	}
	for i, want := range recorded {
		if got := selected[i].Function; got.ID() != want.ID() {
			ins.Err = fmt.Errorf("PCI function %s is %s, but %s was assigned; remove and re-add the assignment if the hardware changed", slot.FunctionAddress(got.Number), got.ID(), want.ID())
			return ins
		}
	}
	for _, device := range selected {
		if ins.Err = h.checkDevice(slot, device); ins.Err != nil {
			return ins
		}
		if !slices.Contains(ins.Groups, device.Group) {
			ins.Groups = append(ins.Groups, device.Group)
		}
	}
	sort.Ints(ins.Groups)
	for _, group := range ins.Groups {
		if err := h.groupBlocker(group); err != nil {
			ins.Err = err
			return ins
		}
		if err := access(filepath.Join(h.vfio, strconv.Itoa(group))); err != nil {
			ins.Err = err
			return ins
		}
	}
	return ins
}

func (h host) checkDevice(slot Slot, device Device) error {
	address := slot.FunctionAddress(device.Number)
	switch {
	case device.bridge():
		return fmt.Errorf("PCI function %s is a bridge, which cannot be assigned", address)
	case !device.PCIe:
		return fmt.Errorf("PCI function %s is a conventional PCI device; only PCI Express devices can be assigned", address)
	case device.Group < 0:
		return fmt.Errorf("PCI function %s has no IOMMU group; enable the IOMMU in the firmware and kernel", address)
	case device.Driver == "":
		return fmt.Errorf("PCI function %s is not bound to a driver; bind it to vfio-pci", address)
	case device.Driver != vfioDriver:
		return fmt.Errorf("PCI function %s is bound to %s; bind it to vfio-pci", address, device.Driver)
	}
	return nil
}

func (h host) checkGroups(groups []int) error {
	for _, group := range groups {
		path := filepath.Join(h.vfio, strconv.Itoa(group))
		viable, err := h.groupViable(path)
		switch {
		case errors.Is(err, syscall.EBUSY):
			return fmt.Errorf("IOMMU group %d is in use by another VM or process", group)
		case err != nil:
			return fmt.Errorf("open IOMMU group %d: %w", group, err)
		case !viable:
			return fmt.Errorf("the kernel reports IOMMU group %d as not viable; every device in the group must be bound to vfio-pci or have no driver", group)
		}
	}
	return nil
}

// groupBlocker names a device that keeps group from being viable.
func (h host) groupBlocker(group int) error {
	dir := filepath.Join(h.groups, strconv.Itoa(group), "devices")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read IOMMU group %d: %w", group, err)
	}
	for _, entry := range entries {
		driver := linkBase(filepath.Join(dir, entry.Name(), "driver"))
		if !groupSafeDrivers[driver] {
			return fmt.Errorf("IOMMU group %d also contains %s, which is bound to %s; bind it to vfio-pci or unbind it", group, entry.Name(), driver)
		}
	}
	return nil
}

func (h host) readSlot(slot Slot) ([]Device, error) {
	entries, err := os.ReadDir(h.devices)
	if err != nil {
		return nil, fmt.Errorf("read PCI devices: %w", err)
	}
	prefix := slot.String() + "."
	var devices []Device
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		number, err := strconv.ParseInt(strings.TrimPrefix(entry.Name(), prefix), 16, 32)
		if err != nil || number < 0 || number > 7 {
			continue
		}
		device, err := readDevice(filepath.Join(h.devices, entry.Name()), int(number))
		if err != nil {
			return nil, err
		}
		devices = append(devices, device)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Number < devices[j].Number })
	return devices, nil
}

func readDevice(dir string, number int) (Device, error) {
	device := Device{Function: Function{Number: number}, Group: -1}
	for _, field := range []struct {
		file string
		dst  *string
	}{{"vendor", &device.Vendor}, {"device", &device.Device}, {"class", &device.Class}} {
		b, err := os.ReadFile(filepath.Join(dir, field.file))
		if err != nil {
			return Device{}, fmt.Errorf("read PCI device %s: %w", filepath.Base(dir), err)
		}
		*field.dst = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(string(b)), "0x"))
	}
	device.Driver = linkBase(filepath.Join(dir, "driver"))
	if group, err := strconv.Atoi(linkBase(filepath.Join(dir, "iommu_group"))); err == nil {
		device.Group = group
	}
	// The kernel creates link attributes only for PCI Express devices.
	_, err := os.Stat(filepath.Join(dir, "max_link_speed"))
	device.PCIe = err == nil
	return device, nil
}

func access(path string) error {
	if err := unix.Access(path, unix.R_OK|unix.W_OK); err != nil {
		return fmt.Errorf("cannot open %s for reading and writing; grant the Voom user access, for example with a udev rule for the kvm group", path)
	}
	return nil
}

func linkBase(path string) string {
	target, err := os.Readlink(path)
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

func (d Device) bridge() bool {
	return strings.HasPrefix(d.Class, "06")
}

func allBridges(devices []Device) bool {
	for _, device := range devices {
		if !device.bridge() {
			return false
		}
	}
	return true
}
