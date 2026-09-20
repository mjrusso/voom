// Package usb discovers Linux USB devices and validates QEMU passthrough declarations.
package usb

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	// QEMU stores the libusb port path in a 16-byte buffer, including its terminator.
	qemuHostPortMaxLen = 15
	qemuDeviceIDPrefix = "voom-usb-"
	qemuDeviceIDMaxLen = 127
)

// Route identifies a stable host USB topology location.
type Route struct {
	Controller string `json:"controller"`
	Protocol   int    `json:"protocol"`
	Port       string `json:"port"`
}

// Location returns the stable Linux USB topology identifier for r.
func (r Route) Location() string {
	return fmt.Sprintf("usb-%s@%d-%s", r.Controller, r.Protocol, r.Port)
}

// Validate checks that r is canonical.
func (r Route) Validate() error {
	if err := validateController(r.Controller); err != nil {
		return err
	}
	if r.Protocol < 1 || r.Protocol > 9 {
		return fmt.Errorf("invalid USB protocol %d", r.Protocol)
	}
	port, err := parsePort(r.Port)
	if err != nil {
		return err
	}
	if port != r.Port {
		return fmt.Errorf("USB port path %q is not canonical", r.Port)
	}
	return nil
}

// Decl assigns a stable host USB route to a name within a VM.
type Decl struct {
	Name string `json:"name"`
	Route
}

// Location returns the stable Linux USB topology identifier for d.
func (d Decl) Location() string {
	return d.Route.Location()
}

// QEMUDeviceID returns the stable QEMU ID for d.
func (d Decl) QEMUDeviceID() string {
	return qemuDeviceIDPrefix + d.Name
}

// Device describes a USB device discovered through Linux sysfs.
type Device struct {
	Route
	Bus          int    `json:"bus"`
	Address      int    `json:"address"`
	VendorID     string `json:"vendorID"`
	ProductID    string `json:"productID"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Product      string `json:"product,omitempty"`
	Serial       string `json:"serial,omitempty"`
	DevicePath   string `json:"devicePath"`
	Accessible   bool   `json:"accessible"`
	AccessError  string `json:"accessError,omitempty"`
}

// Location returns the device's stable Linux USB topology identifier.
func (d Device) Location() string {
	return d.Route.Location()
}

// MarshalJSON includes the canonical location alongside the flattened route fields.
func (d Device) MarshalJSON() ([]byte, error) {
	type device Device
	return json.Marshal(struct {
		Location string `json:"location"`
		device
	}{Location: d.Location(), device: device(d)})
}

// Binding contains the current QEMU selector for a USB declaration.
type Binding struct {
	Name string
	Bus  int
	Port string
}

// QEMUDeviceID returns the stable QEMU ID for b.
func (b Binding) QEMUDeviceID() string {
	return qemuDeviceIDPrefix + b.Name
}

// Validate checks that b is safe to pass to QEMU.
func (b Binding) Validate() error {
	if err := validateName(b.Name); err != nil {
		return err
	}
	if b.Bus < 1 || b.Bus > 255 {
		return fmt.Errorf("invalid USB bus %d", b.Bus)
	}
	port, err := parsePort(b.Port)
	if err != nil {
		return err
	}
	if port != b.Port {
		return fmt.Errorf("USB port path %q is not canonical", b.Port)
	}
	return nil
}

// NewDecl validates a name and stable Linux USB topology location.
func NewDecl(name, location string) (Decl, error) {
	route, err := ParseRoute(location)
	if err != nil {
		return Decl{}, err
	}
	decl := Decl{Name: name, Route: route}
	if err := decl.Validate(); err != nil {
		return Decl{}, err
	}
	return decl, nil
}

// Validate checks that d is safe to persist and resolve.
func (d Decl) Validate() error {
	if err := validateName(d.Name); err != nil {
		return err
	}
	return d.Route.Validate()
}

// ValidateAssignments checks assignment fields and uniqueness.
func ValidateAssignments(assignments []Decl) error {
	names := make(map[string]struct{}, len(assignments))
	routes := make(map[Route]string, len(assignments))
	for i, assignment := range assignments {
		if err := assignment.Validate(); err != nil {
			return fmt.Errorf("USB device %d: %w", i, err)
		}
		if _, exists := names[assignment.Name]; exists {
			return fmt.Errorf("USB device name %q already exists", assignment.Name)
		}
		if owner, exists := routes[assignment.Route]; exists {
			return fmt.Errorf("USB location %s is already assigned to %q", assignment.Location(), owner)
		}
		names[assignment.Name] = struct{}{}
		routes[assignment.Route] = assignment.Name
	}
	return nil
}

// ParseRoute parses a stable Linux USB topology identifier.
func ParseRoute(location string) (Route, error) {
	value := strings.TrimSpace(location)
	if !strings.HasPrefix(value, "usb-") {
		return Route{}, invalidLocationError(location)
	}
	value = strings.TrimPrefix(value, "usb-")
	portSeparator := strings.LastIndexByte(value, '-')
	if portSeparator < 1 || portSeparator == len(value)-1 {
		return Route{}, invalidLocationError(location)
	}
	root, portText := value[:portSeparator], value[portSeparator+1:]
	protocolSeparator := strings.LastIndexByte(root, '@')
	if protocolSeparator < 1 || protocolSeparator == len(root)-1 {
		return Route{}, invalidLocationError(location)
	}
	controller := root[:protocolSeparator]
	if err := validateController(controller); err != nil {
		return Route{}, invalidLocationError(location)
	}
	protocol, err := strconv.Atoi(root[protocolSeparator+1:])
	if err != nil || protocol < 1 || protocol > 9 {
		return Route{}, invalidLocationError(location)
	}
	port, err := parsePort(portText)
	if err != nil {
		return Route{}, invalidLocationError(location)
	}
	return Route{Controller: controller, Protocol: protocol, Port: port}, nil
}

func parsePort(portText string) (string, error) {
	parts := strings.Split(portText, ".")
	if len(parts) > 7 {
		return "", fmt.Errorf("USB port path %q exceeds QEMU's maximum hub depth", portText)
	}
	normalized := make([]string, len(parts))
	for i, part := range parts {
		port, err := strconv.Atoi(part)
		if err != nil || port < 1 || port > 255 {
			return "", fmt.Errorf("invalid USB port path %q", portText)
		}
		normalized[i] = strconv.Itoa(port)
	}
	portText = strings.Join(normalized, ".")
	if len(portText) > qemuHostPortMaxLen {
		return "", fmt.Errorf("USB port path %q is too long for QEMU", portText)
	}
	return portText, nil
}

func invalidLocationError(location string) error {
	return fmt.Errorf("invalid USB location %q; expected usb-<controller>@<protocol>-<port>, for example usb-0000:00:14.0@2-3.2", location)
}

func validateName(name string) error {
	if name == "" {
		return errors.New("USB device name is required")
	}
	maxNameLen := qemuDeviceIDMaxLen - len(qemuDeviceIDPrefix)
	if len(name) > maxNameLen {
		return fmt.Errorf("USB device name %q exceeds %d characters", name, maxNameLen)
	}
	for i, r := range name {
		valid := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if !valid || i == 0 && (r == '_' || r == '-') {
			return fmt.Errorf("invalid USB device name %q", name)
		}
	}
	return nil
}

func validateController(controller string) error {
	if controller == "" || len(controller) > 255 {
		return errors.New("invalid USB controller identity")
	}
	for _, r := range controller {
		valid := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.:+-", r)
		if !valid {
			return fmt.Errorf("invalid USB controller identity %q", controller)
		}
	}
	return nil
}
