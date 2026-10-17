package vm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/mjrusso/voom/internal/host"
	"github.com/mjrusso/voom/internal/state"
)

// MacOSGuestCheck retains command output so failed experiments remain diagnosable.
type MacOSGuestCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
	Error  string `json:"error,omitempty"`
}

// MacOSGuestReport records a point-in-time observation for comparing clone experiments.
type MacOSGuestReport struct {
	SchemaVersion           int               `json:"schemaVersion"`
	CheckedAt               time.Time         `json:"checkedAt"`
	VM                      string            `json:"vm"`
	VMID                    string            `json:"vmId"`
	ImageID                 string            `json:"imageId"`
	MachineIdentifierSHA256 string            `json:"machineIdentifierSha256"`
	MAC                     string            `json:"macAddress"`
	SSHPort                 int               `json:"sshPort"`
	Checks                  []MacOSGuestCheck `json:"checks"`
}

// CheckMacOSGuest collects platform, identity, boot, SSH, and Nix-daemon evidence without provisioning the guest.
func (m *Manager) CheckMacOSGuest(ctx context.Context, name string) (*MacOSGuestReport, error) {
	record, err := m.store.LoadVM(name)
	if err != nil {
		return nil, err
	}
	if record.MacOS == nil {
		return nil, errors.New("macOS guest verification requires a macOS VM")
	}
	report := &MacOSGuestReport{SchemaVersion: 1, CheckedAt: time.Now().UTC(), VM: record.Name, VMID: record.ID, ImageID: record.Image.ID, MachineIdentifierSHA256: record.MacOS.IdentitySHA256, MAC: state.GuestMAC(record.Driver, record.ID), SSHPort: record.Network.SSHPort}
	storage := MacOSGuestCheck{Name: "platform-storage", OK: true}
	if err := record.MacOS.ValidateStorage(m.store.VMDir(record.ID)); err != nil {
		storage.OK = false
		storage.Error = err.Error()
	}
	report.Checks = append(report.Checks, storage)
	running := MacOSGuestCheck{Name: "driver-running", OK: m.IsRunning(record)}
	if !running.OK {
		running.Error = "VM is stopped; start it before collecting guest evidence"
	}
	report.Checks = append(report.Checks, running)
	if !storage.OK || !running.OK {
		return report, errors.New("macOS guest verification failed; see check results")
	}
	executable, err := host.ExePath("ssh")
	if err != nil {
		return report, err
	}
	for _, probe := range macOSGuestProbes {
		args := append(SSHBaseArgs(record), "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "ConnectionAttempts=1", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=2", record.Access.SSHUser+"@"+record.Network.SSHBind, "sh", "-s")
		cmd := exec.CommandContext(ctx, executable, args...)
		cmd.Stdin = strings.NewReader("export PATH=/run/current-system/sw/bin:/nix/var/nix/profiles/default/bin:$PATH\n" + probe.script + "\n")
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		result := MacOSGuestCheck{Name: probe.name, OK: err == nil, Stdout: strings.TrimSpace(stdout.String()), Stderr: strings.TrimSpace(stderr.String())}
		if err != nil {
			result.Error = err.Error()
			if ctx.Err() != nil {
				result.Error = ctx.Err().Error()
			}
		}
		report.Checks = append(report.Checks, result)
		if (probe.name == "ssh-and-architecture" && !result.OK) || ctx.Err() != nil {
			break
		}
	}
	for _, check := range report.Checks {
		if !check.OK {
			return report, fmt.Errorf("macOS guest verification failed at %s; see check results", check.Name)
		}
	}
	return report, nil
}

var macOSGuestProbes = []struct{ name, script string }{
	{"ssh-and-architecture", `arch=$(/usr/bin/uname -m)
printf '%s\n' "$arch"
test "$arch" = arm64`},
	{"macos-version", `version=$(/usr/bin/sw_vers -productVersion) || exit
/usr/bin/sw_vers
test "${version%%.*}" -ge 27`},
	{"hardware-identity", `/usr/sbin/system_profiler SPHardwareDataType -json`},
	{"boot-time", `/usr/sbin/sysctl -n kern.boottime`},
	{"guest-marker", `test -f /etc/mjr-vm-guest`},
	{"active-system", `test -L /run/current-system && test -e /run/current-system && /usr/bin/readlink /run/current-system`},
	{"nix-daemon", `nix --version && nix --extra-experimental-features nix-command store ping --store daemon --json`},
	{"passwordless-sudo", `sudo -n true`},
	{"ssh-host-keys", `found=0
for key in /etc/ssh/ssh_host_*_key.pub; do
    test -f "$key" || continue
    /usr/bin/ssh-keygen -lf "$key" || exit
    found=1
done
test "$found" -eq 1`},
}
