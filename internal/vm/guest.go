package vm

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/mjrusso/voom/internal/bootstrap"
	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/state"
)

// WriteSeedImage builds and writes the cloud-init NoCloud seed image used to
// bootstrap the guest on first boot.
func (m *Manager) WriteSeedImage(vm *state.VMRecord) error {
	keys, err := bootstrap.DiscoverAuthorizedKeys(vm.Access.SSHIdentityPath)
	if err != nil {
		return err
	}
	im, err := m.store.LoadImageByID(vm.Image.ID)
	if err != nil {
		return err
	}
	return bootstrap.WriteNoCloudImage(m.store.SeedImagePath(vm), bootstrap.SeedConfig{
		Hostname:            hostnameFor(vm.Name),
		InstanceID:          "voom-" + vm.ID,
		SSHUser:             vm.Access.SSHUser,
		AuthorizedKeys:      keys,
		InstallGuestHelpers: im.Metadata.InstallGuestHelpers,
	})
}

// ReadGuestPorts loads and validates the guest-published listener report from
// the control share.
func (m *Manager) ReadGuestPorts(vm *state.VMRecord) (*GuestPortsReport, error) {
	path := m.store.ControlSharePortsPath(vm)
	var r GuestPortsReport
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("guest port report unavailable at %s", path)
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("guest port report at %s is malformed: %w", path, err)
	}
	if err := forward.ValidateReport(r); err != nil {
		return nil, err
	}
	return &r, nil
}

// RequireGuestPortReport returns an error if the image lacks the capabilities
// needed for the named guest-reporting feature.
func RequireGuestPortReport(im *state.ImageRecord, feature string) error {
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

// SSHBaseArgs returns the common ssh CLI flags (port, host key options, and
// identity) used to connect to the VM.
func SSHBaseArgs(vm *state.VMRecord) []string {
	args := []string{"-p", strconv.Itoa(vm.Network.SSHPort), "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null"}
	if vm.Access.SSHIdentityPath != "" {
		args = append(args, "-i", vm.Access.SSHIdentityPath)
	}
	return args
}

// GuestAdminUser returns the user account that should run privileged guest
// operations, preferring the NixOS target user when set.
func GuestAdminUser(vm *state.VMRecord) string {
	if vm.Access.NixosTargetUser != "" {
		return vm.Access.NixosTargetUser
	}
	return vm.Access.SSHUser
}

// RunGuestScriptAs pipes the given shell script over SSH to be executed as the
// specified user on the guest, streaming output to log.
func RunGuestScriptAs(vm *state.VMRecord, user, script string, log io.Writer) error {
	args := append(SSHBaseArgs(vm), fmt.Sprintf("%s@%s", user, vm.Network.SSHBind), "sh", "-s")
	cmd := exec.Command("ssh", args...)
	cmd.Stdin = strings.NewReader(script)
	cmd.Stdout = log
	cmd.Stderr = log
	return cmd.Run()
}

func hostnameFor(name string) string {
	host := strings.TrimSpace(name)
	if len(host) > 63 {
		host = host[:63]
	}
	host = strings.TrimRight(host, "-")
	if host == "" {
		return "voom"
	}
	return host
}
