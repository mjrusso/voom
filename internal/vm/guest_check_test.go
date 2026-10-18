package vm

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/share"
	"github.com/mjrusso/voom/internal/state"
)

func TestVerifyGuestReportsCapabilitiesAndFailuresOnBothPlatforms(t *testing.T) {
	for _, system := range []string{"x86_64-linux", "aarch64-darwin"} {
		t.Run(system, func(t *testing.T) {
			st, record, image := guestVerificationFixture(t, system)
			manager := New(st)
			log := filepath.Join(t.TempDir(), "scripts")
			t.Setenv("VERIFY_SCRIPTS", log)
			ssh := filepath.Join(t.TempDir(), "ssh")
			if err := os.WriteFile(ssh, []byte(`#!/bin/sh
script=$(cat)
printf '%s\n' "$script" >> "$VERIFY_SCRIPTS"
if test -n "$VERIFY_FAIL" && printf '%s' "$script" | grep -F -q -- "$VERIFY_FAIL"; then
 echo 'probe output'; echo 'probe error' >&2; exit 1
fi
case "$script" in
 *"exit 77"*) echo 'Optional Nix integration absent'; exit 77 ;;
esac
echo evidence
`), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("VOOM_SSH", ssh)
			report, err := manager.VerifyGuest(context.Background(), record.Name)
			if err != nil {
				t.Fatal(err)
			}
			if report.System != system || report.VMID != record.ID {
				t.Fatalf("wrong identity: %+v", report)
			}
			fingerprint, err := process.RecordFingerprint(st.Runtime(record).VMProcessRecord())
			if err != nil || report.RuntimeFingerprint != fingerprint || report.StorageGeneration != record.StorageGeneration {
				t.Fatalf("report is not bound to the observed runtime: %+v %v", report, err)
			}
			if check := verificationCheck(t, report, "nix"); !check.Skipped || !check.OK {
				t.Fatalf("optional Nix: %+v", check)
			}
			scripts, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(scripts), "voom-control") {
				t.Fatal("unadvertised control share was required")
			}

			image.Capabilities = state.ImageCapabilities{ControlShare: true, GuestShareMount: true, GuestPortReport: true}
			if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(image.ID), "image.json"), image); err != nil {
				t.Fatal(err)
			}
			record.Shares = []share.Decl{{Tag: "project", HostPath: t.TempDir(), GuestPath: "/Volumes/project's files", Readonly: true}}
			record.Network.AutoForward = true
			if err := st.SaveVM(record); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(st.ControlShareDir(record), 0755); err != nil {
				t.Fatal(err)
			}
			ports := forward.PortsReport{SchemaVersion: 1, GeneratedAt: time.Now().Add(-time.Minute)}
			if err := state.WriteJSONAtomic(st.ControlSharePortsPath(record), ports); err != nil {
				t.Fatal(err)
			}
			report, err = manager.VerifyGuest(context.Background(), record.Name)
			if err == nil {
				t.Fatal("stale report and absent watcher passed")
			}
			if verificationCheck(t, report, "listener-report").OK || verificationCheck(t, report, "auto-forward-watcher").OK {
				t.Fatalf("unhealthy integrations passed: %+v", report)
			}

			ports.GeneratedAt = time.Now()
			if err := state.WriteJSONAtomic(st.ControlSharePortsPath(record), ports); err != nil {
				t.Fatal(err)
			}
			watcher := fakeNamedVMProcess(t, "voom", "forward", "auto", "watch", record.ID)
			writeProcessRecord(t, st.Runtime(record).AutoForwardProcessRecord(), watcher)
			report, err = manager.VerifyGuest(context.Background(), record.Name)
			if err != nil {
				t.Fatal(err)
			}
			if !verificationCheck(t, report, "listener-report").OK || !verificationCheck(t, report, "auto-forward-watcher").OK {
				t.Fatal("healthy integrations failed")
			}
			verificationCheck(t, report, "control-share")
			verificationCheck(t, report, "share-project")

			t.Setenv("VERIFY_FAIL", "tag='project'")
			report, err = manager.VerifyGuest(context.Background(), record.Name)
			failed := verificationCheck(t, report, "share-project")
			if err == nil || failed.OK || failed.Stdout != "probe output" || failed.Stderr != "probe error" || failed.Error == "" {
				t.Fatalf("lost mount failure: %+v, %v", failed, err)
			}

			t.Setenv("VERIFY_FAIL", "uname -sm")
			report, err = manager.VerifyGuest(context.Background(), record.Name)
			if err == nil || verificationCheck(t, report, "ssh-and-architecture").OK {
				t.Fatal("SSH failure passed")
			}
			if report.Checks[len(report.Checks)-1].Name != "ssh-and-architecture" {
				t.Fatal("ran further SSH commands after connection failure")
			}
			t.Setenv("VERIFY_FAIL", "")

			if system == "x86_64-linux" {
				image.Capabilities.NixosSwitch = true
				if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(image.ID), "image.json"), image); err != nil {
					t.Fatal(err)
				}
				t.Setenv("VERIFY_FAIL", "nix --version")
				report, err = manager.VerifyGuest(context.Background(), record.Name)
				if err == nil || verificationCheck(t, report, "nix").Skipped {
					t.Fatal("required Nix check was skipped")
				}
			}
			if record.MacOS != nil {
				if err := record.MacOS.ValidateStorage(st.VMStorageDir(record)); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func guestVerificationFixture(t *testing.T, system string) (*state.Store, *state.VMRecord, *state.ImageRecord) {
	t.Helper()
	var st *state.Store
	var record *state.VMRecord
	var image *state.ImageRecord
	if system == "aarch64-darwin" {
		st, record, image = macosResetFixture(t)
		record.Image = state.VMImageRef{ID: image.ID, Name: image.Name}
		if err := st.SaveVM(record); err != nil {
			t.Fatal(err)
		}
	} else {
		st, _ = newTestStore(t)
		image = &state.ImageRecord{SchemaVersion: 1, ID: "image", Name: "linux", Arch: system, Format: "raw", Disk: "disk.raw", Metadata: state.ImageMetadata{SSHUser: "root"}}
		if err := os.MkdirAll(st.ImageDir(image.ID), 0755); err != nil {
			t.Fatal(err)
		}
		if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(image.ID), "image.json"), image); err != nil {
			t.Fatal(err)
		}
		record = &state.VMRecord{SchemaVersion: 1, ID: "linux", Name: "linux", Driver: "qemu", Arch: system, Image: state.VMImageRef{ID: image.ID, Name: image.Name}, Access: state.VMAccess{SSHUser: "root"}, Network: state.VMNetwork{SSHBind: "127.0.0.1", SSHPort: 2222}, Resources: state.VMResources{CPUs: 2, MemoryMiB: 2048}}
		if err := st.SaveVM(record); err != nil {
			t.Fatal(err)
		}
		if err := st.RegisterVM(record.Name, record.ID); err != nil {
			t.Fatal(err)
		}
	}
	driver := "vfkit"
	if record.Driver == "qemu" {
		driver = "qemu-system-x86_64"
	}
	writeProcessRecord(t, st.Runtime(record).VMProcessRecord(), fakeNamedVMProcess(t, driver))
	return st, record, image
}

func verificationCheck(t *testing.T, report *GuestReport, name string) GuestCheck {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("missing %s in %+v", name, report)
	return GuestCheck{}
}

func TestGuestVerificationTimeoutRetainsEvidence(t *testing.T) {
	st, record, _ := guestVerificationFixture(t, "x86_64-linux")
	ssh := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\necho waiting\nexec sleep 20\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VOOM_SSH", ssh)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	report, err := New(st).VerifyGuest(ctx, record.Name)
	if err == nil {
		t.Fatal("timeout passed")
	}
	check := verificationCheck(t, report, "ssh-and-architecture")
	if check.OK || check.Stdout != "waiting" || !strings.Contains(check.Error, "deadline exceeded") {
		t.Fatalf("timeout evidence: %+v", check)
	}
	data, err := json.Marshal(report)
	if err != nil || !strings.Contains(string(data), `"ok":false`) {
		t.Fatalf("failure report: %s %v", data, err)
	}
}

func TestNixChecksAreOptionalUnlessImageRequiresNixOS(t *testing.T) {
	for _, system := range []string{"x86_64-linux", "aarch64-darwin"} {
		record := &state.VMRecord{Arch: system}
		if system == "aarch64-darwin" {
			record.MacOS = &state.MacOSPlatform{}
		}
		for _, required := range []bool{false, true} {
			if required && record.MacOS != nil {
				continue
			}
			image := &state.ImageRecord{Capabilities: state.ImageCapabilities{NixosSwitch: required}}
			for _, probe := range guestProbes(record, image) {
				if probe.name != "nix" && probe.name != "nix-daemon" {
					continue
				}
				cmd := exec.Command("/bin/sh", "-s")
				cmd.Env = []string{"PATH=" + t.TempDir()}
				cmd.Stdin = strings.NewReader(probe.script)
				out, err := cmd.CombinedOutput()
				exit, ok := err.(*exec.ExitError)
				if !ok || (exit.ExitCode() == 77) == required {
					t.Fatalf("%s/%s required=%v: %s %v", system, probe.name, required, out, err)
				}
			}
		}
	}
}

func TestVerifyGuestRejectsChangedRuntime(t *testing.T) {
	for _, system := range []string{"x86_64-linux", "aarch64-darwin"} {
		for _, change := range []string{"driver", "driver-removed", "image", "generation", "auto-forward", "shares", "ssh-access", "capabilities", "resources", "timestamps"} {
			if change == "generation" && system != "aarch64-darwin" {
				continue
			}
			for _, trigger := range []string{"uname -sm", "ssh_host_"} {
				t.Run(system+"/"+change+"/"+trigger, func(t *testing.T) {
					st, record, image := guestVerificationFixture(t, system)
					if change == "auto-forward" || change == "shares" {
						image.Capabilities = state.ImageCapabilities{ControlShare: true, GuestPortReport: true, GuestShareMount: true}
						if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(image.ID), "image.json"), image); err != nil {
							t.Fatal(err)
						}
						if err := state.WriteJSONAtomic(st.ControlSharePortsPath(record), forward.PortsReport{SchemaVersion: 1, GeneratedAt: time.Now()}); err != nil {
							t.Fatal(err)
						}
					}
					dir := t.TempDir()
					target := st.Runtime(record).VMProcessRecord()
					replacement := filepath.Join(dir, "replacement")
					switch change {
					case "driver":
						name := "vfkit"
						if record.Driver == "qemu" {
							name = "qemu-system-x86_64"
						}
						writeProcessRecord(t, replacement, fakeNamedVMProcess(t, name))
					case "driver-removed":
					case "capabilities":
						next := *image
						next.Capabilities.ControlShare = true
						target = filepath.Join(st.ImageDir(image.ID), "image.json")
						if err := state.WriteJSONAtomic(replacement, &next); err != nil {
							t.Fatal(err)
						}
					default:
						next := *record
						switch change {
						case "image":
							next.Image.ID = "replacement-image"
						case "generation":
							next.StorageGeneration = strings.Repeat("b", 32)
						case "auto-forward":
							next.Network.AutoForward = true
						case "shares":
							next.Shares = []share.Decl{{Tag: "new-share", HostPath: t.TempDir(), GuestPath: "/new-share"}}
						case "ssh-access":
							next.Access.SSHUser = "new-user"
						case "resources":
							next.Resources.CPUs++
							next.Resources.MemoryMiB += 1024
						case "timestamps":
							next.CreatedAt = time.Now()
							next.UpdatedAt = time.Now()
						}
						target = filepath.Join(st.VMDir(record.ID), "vm.json")
						if err := state.WriteJSONAtomic(replacement, &next); err != nil {
							t.Fatal(err)
						}
					}
					ssh := filepath.Join(dir, "ssh")
					if err := os.WriteFile(ssh, []byte(`#!/bin/sh
set -eu
script=$(cat)
if printf '%s' "$script" | grep -F -q "$VERIFY_TRIGGER"; then
 if test -f "$VERIFY_REPLACEMENT"; then
  mv "$VERIFY_REPLACEMENT" "$VERIFY_TARGET"
 else
  rm -f "$VERIFY_TARGET"
 fi
fi
echo evidence
`), 0o700); err != nil {
						t.Fatal(err)
					}
					t.Setenv("VOOM_SSH", ssh)
					t.Setenv("VERIFY_TRIGGER", trigger)
					t.Setenv("VERIFY_REPLACEMENT", replacement)
					t.Setenv("VERIFY_TARGET", target)
					report, err := New(st).VerifyGuest(context.Background(), record.Name)
					if change == "timestamps" {
						if err != nil {
							t.Fatalf("audit timestamps changed verification: %v", err)
						}
						return
					}
					if err == nil {
						t.Fatal("verification combined evidence across a runtime change")
					}
					if check := verificationCheck(t, report, "runtime-consistency"); check.OK || check.Error == "" {
						t.Fatalf("runtime change missing from report: %+v", check)
					}
					if check := verificationCheck(t, report, "ssh-and-architecture"); !check.OK || check.Stdout != "evidence" {
						t.Fatalf("collected evidence was lost: %+v", check)
					}
				})
			}
		}
	}
}
