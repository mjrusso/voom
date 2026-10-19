package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"time"

	"github.com/mjrusso/voom/internal/host"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/share"
	"github.com/mjrusso/voom/internal/state"
)

// GuestCheck retains diagnostic output, including failures.
type GuestCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped,omitempty"`
	Stdout  string `json:"stdout,omitempty"`
	Stderr  string `json:"stderr,omitempty"`
	Error   string `json:"error,omitempty"`
}

// GuestReport records a read-only observation of a managed guest.
type GuestReport struct {
	SchemaVersion           int          `json:"schemaVersion"`
	CheckedAt               time.Time    `json:"checkedAt"`
	VM                      string       `json:"vm"`
	System                  string       `json:"system"`
	VMID                    string       `json:"vmId"`
	ImageID                 string       `json:"imageId"`
	MachineIdentifierSHA256 string       `json:"machineIdentifierSha256,omitempty"`
	MAC                     string       `json:"macAddress"`
	SSHPort                 int          `json:"sshPort"`
	RuntimeFingerprint      string       `json:"runtimeFingerprint,omitempty"`
	StorageGeneration       string       `json:"storageGeneration,omitempty"`
	Checks                  []GuestCheck `json:"checks"`
}

// VerifyGuest checks a running guest without provisioning it or changing its lifecycle.
func (m *Manager) VerifyGuest(ctx context.Context, name string) (*GuestReport, error) {
	record, unlock, err := m.store.LockVMRecord(ctx, name)
	if err != nil {
		return nil, err
	}
	defer unlock()
	report := &GuestReport{SchemaVersion: 1, CheckedAt: time.Now().UTC(), VM: record.Name, VMID: record.ID, ImageID: record.Image.ID, System: record.Arch, MAC: state.GuestMAC(record.Driver, record.ID), SSHPort: record.Network.SSHPort}
	if record.MacOS != nil {
		report.MachineIdentifierSHA256 = record.MacOS.IdentitySHA256
		storage := GuestCheck{Name: "platform-storage", OK: true}
		if err := record.MacOS.ValidateStorage(m.store.VMStorageDir(record)); err != nil {
			storage.OK = false
			storage.Error = err.Error()
		}
		report.Checks = append(report.Checks, storage)
	}
	driver, inspectionErr := process.ObserveRecord(m.store.Runtime(record).VMProcessRecord(), state.VMProcessKind(record.Driver), "")
	running := GuestCheck{Name: "driver-running", OK: inspectionErr == nil && driver.State == process.RecordRunning}
	if inspectionErr != nil {
		running.Error = inspectionErr.Error()
	} else if !running.OK {
		running.Error = "VM is stopped; start it before collecting guest evidence"
	}
	report.Checks = append(report.Checks, running)
	if err := report.failure(); err != nil {
		return report, err
	}
	image, err := m.store.LoadImageByID(record.Image.ID)
	if err != nil {
		report.Checks = append(report.Checks, GuestCheck{Name: "image-capabilities", Error: err.Error()})
		return report, fmt.Errorf("load image capabilities: %w", err)
	}
	snapshot := captureVerificationSnapshot(record, image, driver.Fingerprint)
	report.RuntimeFingerprint = snapshot.RuntimeFingerprint
	report.StorageGeneration = snapshot.VM.StorageGeneration
	unlock()
	if err := errors.Join(ValidateGuestDeclarations(record), validateImageFeatures(record, image)); err != nil {
		report.Checks = append(report.Checks, GuestCheck{Name: "image-capabilities", Error: err.Error()})
		if record.MacOS != nil {
			return report, report.failure()
		}
	}
	if image.Capabilities.GuestPortReport {
		ports, err := m.ReadGuestPorts(record)
		check := GuestCheck{Name: "listener-report", OK: err == nil}
		if err != nil {
			check.Error = err.Error()
		} else {
			data, _ := json.Marshal(ports)
			check.Stdout = string(data)
		}
		report.Checks = append(report.Checks, check)
	}
	if record.Network.AutoForward {
		_, alive := process.ValidRecord(m.store.Runtime(record).AutoForwardProcessRecord(), "auto-forward")
		check := GuestCheck{Name: "auto-forward-watcher", OK: alive}
		if !alive {
			check.Error = "automatic forwarding is enabled but its watcher is not running"
		}
		report.Checks = append(report.Checks, check)
	}
	executable, err := host.ExePath("ssh")
	if err != nil {
		report.Checks = append(report.Checks, GuestCheck{Name: "ssh-and-architecture", Error: err.Error()})
		return report, err
	}
	for _, probe := range guestProbes(record, image) {
		args := append(SSHBaseArgs(record), "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "ConnectionAttempts=1", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=2", record.Access.SSHUser+"@"+record.Network.SSHBind, "sh", "-s")
		cmd := exec.CommandContext(ctx, executable, args...)
		cmd.Stdin = strings.NewReader("export PATH=/run/current-system/sw/bin:/nix/var/nix/profiles/default/bin:$PATH\n" + probe.script + "\n")
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		result := GuestCheck{Name: probe.name, OK: err == nil, Stdout: strings.TrimSpace(stdout.String()), Stderr: strings.TrimSpace(stderr.String())}
		if err != nil {
			result.Error = err.Error()
			if ctx.Err() != nil {
				result.Error = ctx.Err().Error()
			}
		}
		var exit *exec.ExitError
		if probe.optional && errors.As(err, &exit) && exit.ExitCode() == 77 {
			result.OK, result.Skipped, result.Error = true, true, ""
		}
		report.Checks = append(report.Checks, result)
		if err := m.checkGuestRuntime(ctx, record.Name, snapshot); err != nil {
			report.Checks = append(report.Checks, GuestCheck{Name: "runtime-consistency", Error: err.Error()})
			break
		}
		if (probe.name == "ssh-and-architecture" && !result.OK) || ctx.Err() != nil {
			break
		}
	}
	return report, report.failure()
}

type verificationSnapshot struct {
	VM                 state.VMRecord
	Capabilities       state.ImageCapabilities
	RuntimeFingerprint string
}

func captureVerificationSnapshot(record *state.VMRecord, image *state.ImageRecord, fingerprint string) verificationSnapshot {
	vm := *record
	vm.CreatedAt, vm.UpdatedAt = time.Time{}, time.Time{}
	return verificationSnapshot{VM: vm, Capabilities: image.Capabilities, RuntimeFingerprint: fingerprint}
}

func (m *Manager) checkGuestRuntime(ctx context.Context, name string, expected verificationSnapshot) error {
	record, unlock, err := m.store.LockVMRecord(ctx, name)
	if err != nil {
		return fmt.Errorf("cannot confirm guest runtime: %w", err)
	}
	defer unlock()
	image, err := m.store.LoadImageByID(record.Image.ID)
	if err != nil {
		return fmt.Errorf("cannot confirm guest image: %w", err)
	}
	driver, err := process.ObserveRecord(m.store.Runtime(record).VMProcessRecord(), state.VMProcessKind(record.Driver), expected.RuntimeFingerprint)
	if err != nil {
		return fmt.Errorf("cannot confirm guest runtime: %w", err)
	}
	observed := captureVerificationSnapshot(record, image, driver.Fingerprint)
	if driver.State != process.RecordRunning || !reflect.DeepEqual(observed, expected) {
		return errors.New("guest configuration or runtime changed or stopped during verification; retry verification")
	}
	return nil
}

func (r *GuestReport) failure() error {
	for _, check := range r.Checks {
		if !check.OK {
			return fmt.Errorf("guest verification failed at %s; see check results", check.Name)
		}
	}
	return nil
}

type guestProbe struct {
	name, script string
	optional     bool
}

func guestProbes(record *state.VMRecord, image *state.ImageRecord) []guestProbe {
	kernel, architecture := "Linux", strings.Split(record.Arch, "-")[0]
	if record.MacOS != nil {
		kernel, architecture = "Darwin", "arm64"
	}
	probes := []guestProbe{{name: "ssh-and-architecture", script: fmt.Sprintf(`uname -sm
test "$(uname -s)" = %q && test "$(uname -m)" = %q`, kernel, architecture)}}
	if record.MacOS != nil {
		probes = append(probes, guestProbe{name: "macos-version", script: `version=$(/usr/bin/sw_vers -productVersion) || exit
/usr/bin/sw_vers
test "${version%%.*}" -ge 27`},
			guestProbe{name: "hardware-identity", script: `/usr/sbin/system_profiler SPHardwareDataType -json`},
			guestProbe{name: "boot-time", script: `/usr/sbin/sysctl -n kern.boottime`})
	} else {
		probes = append(probes, guestProbe{name: "linux-version", script: `cat /etc/os-release && uname -r`},
			guestProbe{name: "boot-time", script: `cat /proc/sys/kernel/random/boot_id && grep '^btime ' /proc/stat`})
	}
	activeGuard, nixGuard, daemonGuard := "", "", ""
	nixRequired := image.Capabilities.NixosSwitch
	if !nixRequired {
		activeGuard = `if ! test -e /run/current-system && ! test -L /run/current-system; then echo 'No active Nix system'; exit 77; fi
`
		nixGuard = `command -v nix >/dev/null 2>&1 || { echo 'Nix is not installed'; exit 77; }
`
		daemonGuard = `if ! test -S /nix/var/nix/daemon-socket/socket &&
 ! test -e /Library/LaunchDaemons/org.nixos.nix-daemon.plist &&
 ! test -e /etc/systemd/system/nix-daemon.service &&
 ! test -e /etc/systemd/system/nix-daemon.socket &&
 ! test -e /usr/lib/systemd/system/nix-daemon.service; then
 echo 'No Nix daemon detected'; exit 77
fi
`
	}
	probes = append(probes, guestProbe{name: "active-system", script: activeGuard + `test -L /run/current-system && test -e /run/current-system && readlink /run/current-system`, optional: !nixRequired},
		guestProbe{name: "nix", script: nixGuard + `nix --version`, optional: !nixRequired},
		guestProbe{name: "nix-daemon", script: nixGuard + daemonGuard + `nix --extra-experimental-features nix-command store info --store daemon --json`, optional: !nixRequired})
	probes = append(probes, guestProbe{name: "passwordless-sudo", script: `test "$(id -u)" = 0 || sudo -n true`},
		guestProbe{name: "ssh-host-keys", script: `found=0
for key in /etc/ssh/ssh_host_*_key.pub; do
 test -f "$key" || continue
 ssh-keygen -lf "$key" || exit
 found=1
done
test "$found" -eq 1`})
	if image.Capabilities.ControlShare {
		probes = append(probes, guestProbe{name: "control-share", script: share.GuestMountVerifyScript(state.ControlShareTag, state.ControlShareGuestPath, false)})
	}
	for _, sh := range record.Shares {
		probes = append(probes, guestProbe{name: "share-" + sh.Tag, script: share.GuestMountVerifyScript(sh.Tag, sh.GuestPath, sh.Readonly)})
	}
	return probes
}
