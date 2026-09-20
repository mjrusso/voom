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
	name := ExecutableName(arch)
	path, err := host.ExePath(name)
	if err != nil {
		return fmt.Errorf("missing required command: %s", name)
	}
	out, err := exec.Command(path, "-device", "help").CombinedOutput()
	if err != nil {
		return fmt.Errorf("inspect %s USB support: %w", name, err)
	}
	for _, device := range []string{"qemu-xhci", "usb-host"} {
		if !strings.Contains(string(out), `name "`+device+`"`) {
			return fmt.Errorf("%s does not provide required device %s; install a QEMU build with USB and libusb support", name, device)
		}
	}
	return nil
}
