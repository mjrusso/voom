package vm

import (
	"fmt"
	"strings"

	"github.com/mjrusso/voom/internal/state"
)

// ValidateGuestDeclarations checks VM-owned feature policy without loading an image.
func ValidateGuestDeclarations(vm *state.VMRecord) error {
	if vm.MacOS != nil && (len(vm.Shares) > 0 || len(vm.Network.Forwards) > 0 || vm.Network.AutoForward || vm.Network.Egress != nil) {
		return fmt.Errorf("MacOS guests do not support user shares, application forwards, automatic forwarding, or egress attachments; remove these declarations before starting, resetting, or cloning")
	}
	return nil
}

func validateImageFeatures(vm *state.VMRecord, im *state.ImageRecord) error {
	if vm.MacOS != nil {
		return state.ValidateMacOSCapabilities(im.Capabilities)
	}
	if len(vm.Shares) > 0 {
		if err := requireShareMount(im); err != nil {
			return err
		}
	}
	if vm.Network.AutoForward {
		return RequireGuestPortReport(im, "auto-forwarding")
	}
	return nil
}

func requireShareMount(im *state.ImageRecord) error {
	if !im.Capabilities.ControlShare || !im.Capabilities.GuestShareMount {
		return fmt.Errorf("share mounting unavailable for image %q: requires controlShare and guestShareMount capabilities; import an image with guest share-mount support", im.Name)
	}
	return nil
}

// RequireGuestPortReport returns an error if the image lacks the capabilities
// needed for the named guest-reporting feature.
func RequireGuestPortReport(im *state.ImageRecord, feature string) error {
	if im.MacOS != nil {
		return fmt.Errorf("%s is not supported for MacOS guests", feature)
	}
	missing := []string{}
	if !im.Capabilities.ControlShare {
		missing = append(missing, "controlShare capability is false")
	}
	if !im.Capabilities.GuestPortReport {
		missing = append(missing, "guestPortReport capability is false")
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%s unavailable for image %q: %s; import an image with the reserved voom-control share and voom-portfwd support", feature, im.Name, strings.Join(missing, "; "))
}
