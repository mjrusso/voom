// Package pci discovers Linux PCI Express devices and checks them for VFIO
// passthrough.
package pci

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	// allFunctions selects every function of a slot.
	allFunctions = -1
	// maxAssignments is the number of root ports that fit in the one
	// multifunction guest slot QEMU arguments place them in.
	maxAssignments = 8
	maxNameLen     = 64
)

// Slot identifies a host PCI device by domain, bus, and device number.
type Slot struct {
	Domain int
	Bus    int
	Device int
}

// parseSlot parses a slot address such as 0000:41:00 or 41:00.
func parseSlot(text string) (Slot, error) {
	parts := strings.Split(strings.TrimSpace(text), ":")
	if len(parts) == 2 {
		parts = append([]string{"0000"}, parts...)
	}
	if len(parts) != 3 {
		return Slot{}, invalidSlotError(text)
	}
	limits := []int64{0xffff, 0xff, 0x1f}
	values := make([]int, 3)
	for i, part := range parts {
		value, err := strconv.ParseInt(part, 16, 32)
		if err != nil || part == "" || value < 0 || value > limits[i] {
			return Slot{}, invalidSlotError(text)
		}
		values[i] = int(value)
	}
	return Slot{Domain: values[0], Bus: values[1], Device: values[2]}, nil
}

func invalidSlotError(text string) error {
	return fmt.Errorf("invalid PCI slot %q; expected [domain:]bus:device, for example 0000:41:00", text)
}

func (s Slot) String() string {
	return fmt.Sprintf("%04x:%02x:%02x", s.Domain, s.Bus, s.Device)
}

// FunctionAddress returns the sysfs address of function n in s.
func (s Slot) FunctionAddress(n int) string {
	return fmt.Sprintf("%s.%x", s, n)
}

// MarshalText encodes s in its canonical form.
func (s Slot) MarshalText() ([]byte, error) {
	return []byte(s.String()), nil
}

// UnmarshalText parses a slot address.
func (s *Slot) UnmarshalText(text []byte) error {
	slot, err := parseSlot(string(text))
	if err != nil {
		return err
	}
	*s = slot
	return nil
}

// Selector is a slot, or one function of a slot.
type Selector struct {
	Slot     Slot
	Function int
}

// ParseSelector parses a slot (all functions) or a function address such as
// 0000:41:00.0.
func ParseSelector(text string) (Selector, error) {
	slotText, functionText, hasFunction := strings.Cut(strings.TrimSpace(text), ".")
	slot, err := parseSlot(slotText)
	if err != nil {
		return Selector{}, err
	}
	if !hasFunction {
		return Selector{Slot: slot, Function: allFunctions}, nil
	}
	function, err := strconv.ParseInt(functionText, 16, 32)
	if err != nil || function < 0 || function > 7 {
		return Selector{}, fmt.Errorf("invalid PCI function in %q; expected 0-7", text)
	}
	return Selector{Slot: slot, Function: int(function)}, nil
}

func (s Selector) String() string {
	if s.Function == allFunctions {
		return s.Slot.String()
	}
	return s.Slot.FunctionAddress(s.Function)
}

// Function is one PCI function and the identity it had when assigned.
type Function struct {
	Number int    `json:"number"`
	Vendor string `json:"vendor"`
	Device string `json:"device"`
}

// ID returns the vendor:device identity of f.
func (f Function) ID() string {
	return f.Vendor + ":" + f.Device
}

// Decl assigns functions of one host slot to a name within a VM.
type Decl struct {
	Name      string     `json:"name"`
	Slot      Slot       `json:"slot"`
	Functions []Function `json:"functions"`
}

// Selector returns the selector that reproduces d with `voom pci add`.
func (d Decl) Selector() Selector {
	if len(d.Functions) == 1 {
		return Selector{Slot: d.Slot, Function: d.Functions[0].Number}
	}
	return Selector{Slot: d.Slot, Function: allFunctions}
}

// Validate checks that d is safe to persist and pass to QEMU.
func (d Decl) Validate() error {
	if err := validateName(d.Name); err != nil {
		return err
	}
	if len(d.Functions) == 0 {
		return fmt.Errorf("PCI device %q has no functions", d.Name)
	}
	if len(d.Functions) > 1 && d.Functions[0].Number != 0 {
		return fmt.Errorf("PCI device %q assigns several functions of %s without function 0", d.Name, d.Slot)
	}
	for i, f := range d.Functions {
		if f.Number < 0 || f.Number > 7 || i > 0 && f.Number <= d.Functions[i-1].Number {
			return fmt.Errorf("PCI device %q has invalid or unsorted function numbers", d.Name)
		}
		if !isHexID(f.Vendor) || !isHexID(f.Device) {
			return fmt.Errorf("PCI device %q has invalid identity %q for function %d", d.Name, f.ID(), f.Number)
		}
	}
	return nil
}

// ValidateAssignments checks assignment fields, uniqueness, and the
// assignment limit.
func ValidateAssignments(assignments []Decl) error {
	if len(assignments) > maxAssignments {
		return fmt.Errorf("a VM can have at most %d PCI assignments", maxAssignments)
	}
	names := make(map[string]struct{}, len(assignments))
	slots := make(map[Slot]string, len(assignments))
	for _, assignment := range assignments {
		if err := assignment.Validate(); err != nil {
			return err
		}
		if _, exists := names[assignment.Name]; exists {
			return fmt.Errorf("PCI device name %q already exists", assignment.Name)
		}
		if owner, exists := slots[assignment.Slot]; exists {
			return fmt.Errorf("PCI slot %s is already assigned to %q", assignment.Slot, owner)
		}
		names[assignment.Name] = struct{}{}
		slots[assignment.Slot] = assignment.Name
	}
	return nil
}

func validateName(name string) error {
	if name == "" {
		return errors.New("PCI device name is required")
	}
	if len(name) > maxNameLen {
		return fmt.Errorf("PCI device name %q exceeds %d characters", name, maxNameLen)
	}
	for i, r := range name {
		valid := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if !valid || i == 0 && (r == '_' || r == '-') {
			return fmt.Errorf("invalid PCI device name %q", name)
		}
	}
	return nil
}

func isHexID(s string) bool {
	if len(s) != 4 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
