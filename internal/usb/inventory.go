package usb

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

const (
	defaultSysfsRoot = "/sys/bus/usb/devices"
	defaultDevRoot   = "/dev/bus/usb"
)

var errNotConnected = errors.New("USB device is not connected")

type controllerRoute struct {
	controller string
	protocol   int
}

type runtimeRoute struct {
	bus  int
	port string
}

type rootHub struct {
	controller string
	protocol   int
}

// Inventory is one immutable view of Linux USB topology and device access.
type Inventory struct {
	buses            map[controllerRoute][]int
	controllerErrors map[string]error
	routeErrors      map[controllerRoute]error
	devices          map[runtimeRoute]Device
	deviceErrors     map[runtimeRoute]error
	discovered       []Device
}

// Scan reads the current Linux USB inventory.
func Scan() (*Inventory, error) {
	if runtime.GOOS != "linux" {
		return nil, errors.New("USB inventory is supported only on Linux")
	}
	return scanInventory(defaultSysfsRoot, defaultDevRoot)
}

func scanInventory(sysfsRoot, devRoot string) (*Inventory, error) {
	entries, err := os.ReadDir(sysfsRoot)
	if err != nil {
		return nil, fmt.Errorf("read USB sysfs devices: %w", err)
	}
	inventory := &Inventory{
		buses:            make(map[controllerRoute][]int),
		controllerErrors: make(map[string]error),
		routeErrors:      make(map[controllerRoute]error),
		devices:          make(map[runtimeRoute]Device),
		deviceErrors:     make(map[runtimeRoute]error),
	}
	hubs := make(map[int]rootHub)
	for _, entry := range entries {
		if !isRootHubName(entry.Name()) {
			continue
		}
		dir := filepath.Join(sysfsRoot, entry.Name())
		controller, err := readText(filepath.Join(dir, "serial"))
		if err != nil {
			continue
		}
		protocol, err := readRootProtocol(filepath.Join(dir, "version"))
		if err != nil {
			inventory.controllerErrors[controller] = fmt.Errorf("read USB protocol for controller %q: %w", controller, err)
			continue
		}
		route := controllerRoute{controller: controller, protocol: protocol}
		busNumber, err := readDecimal(filepath.Join(dir, "busnum"))
		if err != nil {
			inventory.routeErrors[route] = fmt.Errorf("read USB bus for controller %q: %w", controller, err)
			continue
		}
		inventory.buses[route] = append(inventory.buses[route], busNumber)
		hubs[busNumber] = rootHub{controller: controller, protocol: protocol}
	}
	for _, entry := range entries {
		bus, port, err := parseRuntimeLocation(entry.Name())
		if err != nil {
			continue
		}
		route := runtimeRoute{bus: bus, port: port}
		device, err := readDevice(sysfsRoot, devRoot, hubs, bus, port)
		if errors.Is(err, errNotConnected) {
			continue
		}
		if err != nil {
			inventory.deviceErrors[route] = err
			continue
		}
		inventory.devices[route] = device
		inventory.discovered = append(inventory.discovered, device)
	}
	sort.Slice(inventory.discovered, func(i, j int) bool {
		return inventory.discovered[i].Route.Location() < inventory.discovered[j].Route.Location()
	})
	return inventory, nil
}

// Devices returns the non-hub devices present when the inventory was scanned.
func (i *Inventory) Devices() []Device {
	return append([]Device(nil), i.discovered...)
}

// CheckAccess verifies that d's controller route exists and any connected device is accessible.
func (i *Inventory) CheckAccess(d Decl) error {
	device, connected, err := i.Inspect(d)
	if err != nil || !connected {
		return err
	}
	return checkDeviceAccess(d, device)
}

// Resolve returns d's host-bus selector from this inventory.
func (i *Inventory) Resolve(d Decl) (Binding, error) {
	binding, device, connected, err := i.inspect(d)
	if err != nil {
		return Binding{}, err
	}
	if connected {
		if err := checkDeviceAccess(d, device); err != nil {
			return Binding{}, err
		}
	}
	return binding, nil
}

// Inspect returns the device present at d's route in this inventory.
func (i *Inventory) Inspect(d Decl) (Device, bool, error) {
	_, device, connected, err := i.inspect(d)
	return device, connected, err
}

func (i *Inventory) inspect(d Decl) (Binding, Device, bool, error) {
	if err := d.Validate(); err != nil {
		return Binding{}, Device{}, false, err
	}
	binding, err := i.binding(d)
	if err != nil {
		return Binding{}, Device{}, false, err
	}
	if err := binding.Validate(); err != nil {
		return Binding{}, Device{}, false, err
	}
	route := runtimeRoute{bus: binding.Bus, port: binding.Port}
	if err := i.deviceErrors[route]; err != nil {
		return Binding{}, Device{}, false, err
	}
	device, connected := i.devices[route]
	return binding, device, connected, nil
}

func (i *Inventory) binding(d Decl) (Binding, error) {
	if err := i.controllerErrors[d.Controller]; err != nil {
		return Binding{}, err
	}
	route := controllerRoute{controller: d.Controller, protocol: d.Protocol}
	if err := i.routeErrors[route]; err != nil {
		return Binding{}, err
	}
	buses := i.buses[route]
	if len(buses) == 0 {
		return Binding{}, fmt.Errorf("USB controller route %s is not available", d.Location())
	}
	if len(buses) > 1 {
		return Binding{}, fmt.Errorf("USB controller route %s is ambiguous", d.Location())
	}
	return Binding{Name: d.Name, Bus: buses[0], Port: d.Port}, nil
}

func checkDeviceAccess(d Decl, device Device) error {
	if device.Accessible {
		return nil
	}
	return fmt.Errorf("USB device %q at %s is not accessible through %s: %s; configure a udev rule or ACL for the Voom user", d.Name, d.Location(), device.DevicePath, device.AccessError)
}

func readDevice(sysfsRoot, devRoot string, hubs map[int]rootHub, bus int, port string) (Device, error) {
	runtimeLocation := fmt.Sprintf("%d-%s", bus, port)
	dir := filepath.Join(sysfsRoot, runtimeLocation)
	deviceClass, err := readText(filepath.Join(dir, "bDeviceClass"))
	if errors.Is(err, os.ErrNotExist) {
		return Device{}, fmt.Errorf("%w at %s", errNotConnected, runtimeLocation)
	}
	if err != nil {
		return Device{}, fmt.Errorf("read USB device %s: %w", runtimeLocation, err)
	}
	if strings.EqualFold(deviceClass, "09") {
		return Device{}, fmt.Errorf("USB location %s contains a hub, which QEMU cannot pass through", runtimeLocation)
	}
	busNumber, err := readDecimal(filepath.Join(dir, "busnum"))
	if err != nil {
		return Device{}, fmt.Errorf("read USB bus for %s: %w", runtimeLocation, err)
	}
	hub, ok := hubs[busNumber]
	if !ok {
		return Device{}, fmt.Errorf("read USB controller for %s: root hub %d is unavailable", runtimeLocation, busNumber)
	}
	address, err := readDecimal(filepath.Join(dir, "devnum"))
	if err != nil {
		return Device{}, fmt.Errorf("read USB address for %s: %w", runtimeLocation, err)
	}
	vendor, err := readText(filepath.Join(dir, "idVendor"))
	if err != nil {
		return Device{}, fmt.Errorf("read USB vendor for %s: %w", runtimeLocation, err)
	}
	productID, err := readText(filepath.Join(dir, "idProduct"))
	if err != nil {
		return Device{}, fmt.Errorf("read USB product for %s: %w", runtimeLocation, err)
	}
	devicePath := filepath.Join(devRoot, fmt.Sprintf("%03d", busNumber), fmt.Sprintf("%03d", address))
	device := Device{
		Route:        Route{Controller: hub.controller, Protocol: hub.protocol, Port: port},
		Bus:          busNumber,
		Address:      address,
		VendorID:     strings.ToLower(vendor),
		ProductID:    strings.ToLower(productID),
		Manufacturer: readOptionalText(filepath.Join(dir, "manufacturer")),
		Product:      readOptionalText(filepath.Join(dir, "product")),
		Serial:       readOptionalText(filepath.Join(dir, "serial")),
		DevicePath:   devicePath,
	}
	f, accessErr := os.OpenFile(devicePath, os.O_RDWR, 0)
	if accessErr == nil {
		device.Accessible = true
		_ = f.Close()
	} else {
		device.AccessError = accessErr.Error()
	}
	return device, nil
}

func isRootHubName(name string) bool {
	if !strings.HasPrefix(name, "usb") {
		return false
	}
	bus, err := strconv.Atoi(strings.TrimPrefix(name, "usb"))
	return err == nil && bus > 0
}

func readRootProtocol(path string) (int, error) {
	version, err := readText(path)
	if err != nil {
		return 0, err
	}
	major, _, _ := strings.Cut(version, ".")
	protocol, err := strconv.Atoi(major)
	if err != nil || protocol < 1 || protocol > 9 {
		return 0, fmt.Errorf("invalid USB version %q", version)
	}
	return protocol, nil
}

func parseRuntimeLocation(location string) (int, string, error) {
	busText, portText, ok := strings.Cut(location, "-")
	bus, err := strconv.Atoi(busText)
	if !ok || err != nil || bus < 1 || bus > 255 {
		return 0, "", fmt.Errorf("invalid runtime USB location %q", location)
	}
	port, err := parsePort(portText)
	if err != nil {
		return 0, "", err
	}
	return bus, port, nil
}

func readText(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func readOptionalText(path string) string {
	s, _ := readText(path)
	return s
}

func readDecimal(path string) (int, error) {
	s, err := readText(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(s)
}
