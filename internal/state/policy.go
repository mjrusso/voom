package state

import (
	"fmt"
	"runtime"

	"github.com/mjrusso/voom/internal/host"
)

const (
	// SchemaVersion is the on-disk schema version this build reads and writes.
	SchemaVersion = 1
	// SSHLow is the inclusive low end of the SSH host-port allocation range.
	SSHLow = 2222
	// SSHHigh is the inclusive high end of the SSH host-port allocation range.
	SSHHigh = 2299
	// ControlShareTag is the virtiofs share tag used for the host->guest control share.
	ControlShareTag  = "voom-control"
	qemuGuestIP      = "192.168.127.3"
	vfkitGuestIP     = "192.168.127.3"
	controlSharePath = "/run/voom"
)

// DefaultDriver returns the default VM driver for the current host OS ("qemu" on Linux, "vfkit" elsewhere).
func DefaultDriver() string {
	if runtime.GOOS == "linux" {
		return "qemu"
	}
	return "vfkit"
}

// VMProcessKind returns the process kind label ("vfkit" or "qemu") for the given driver.
func VMProcessKind(driver string) string {
	if driver == "vfkit" {
		return "vfkit"
	}
	return "qemu"
}

// ValidateImageArch returns an error if arch does not match the host system architecture.
func ValidateImageArch(arch string) error {
	if arch != host.System() {
		return fmt.Errorf("cross-arch emulation is not supported: image %s on host %s", arch, host.System())
	}
	return nil
}

// DefaultGuestTargetIP returns the IP address the host uses to reach the guest under the given driver.
func DefaultGuestTargetIP(driver string) string {
	if driver == "vfkit" {
		return vfkitGuestIP
	}
	return qemuGuestIP
}

// GuestMAC returns a deterministic guest MAC address derived from id, using a driver-specific OUI prefix.
func GuestMAC(driver, id string) string {
	if driver == "vfkit" {
		return localAdminMACFor(id)
	}
	return macFor(id)
}

// ControlShareGuestPath returns the guest-side mount path of the control share.
func ControlShareGuestPath() string {
	return controlSharePath
}

func macFor(seed string) string {
	b := []byte(seed)
	if len(b) < 5 {
		b = append(b, make([]byte, 5-len(b))...)
	}
	return fmt.Sprintf("52:54:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3])
}

func localAdminMACFor(seed string) string {
	b := []byte(seed)
	if len(b) < 5 {
		b = append(b, make([]byte, 5-len(b))...)
	}
	return fmt.Sprintf("02:%02x:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3], b[4])
}
