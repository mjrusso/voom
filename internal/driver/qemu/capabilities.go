package qemu

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/mjrusso/voom/internal/host"
)

// ExecutableName returns the qemu-system executable name for arch.
func ExecutableName(arch string) string {
	return "qemu-system-" + strings.TrimSuffix(arch, "-linux")
}

// RequireUSB verifies that QEMU for arch supports XHCI and libusb host passthrough.
func RequireUSB(arch string) error {
	return requireDevices(arch, "USB and libusb", "qemu-xhci", "usb-host")
}

// RequireVFIO verifies that QEMU for arch supports PCI Express root ports and
// VFIO PCI passthrough.
func RequireVFIO(arch string) error {
	return requireDevices(arch, "VFIO", "pcie-root-port", "vfio-pci")
}

func requireDevices(arch, feature string, devices ...string) error {
	name := ExecutableName(arch)
	path, err := host.ExePath(name)
	if err != nil {
		return fmt.Errorf("missing required command: %s", name)
	}
	out, err := exec.Command(path, "-device", "help").CombinedOutput()
	if err != nil {
		return fmt.Errorf("inspect %s device support: %w", name, err)
	}
	for _, device := range devices {
		if !strings.Contains(string(out), `name "`+device+`"`) {
			return fmt.Errorf("%s does not provide required device %s; install a QEMU build with %s support", name, device, feature)
		}
	}
	return nil
}
