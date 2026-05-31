// Package doctor runs voom's environment self-checks: required executables,
// host capabilities, and state directory consistency.
package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/mjrusso/voom/internal/host"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
	"github.com/mjrusso/voom/internal/vm"
)

// Check is a single doctor result describing a named environment check, its
// severity (ok, warn, or fatal), pass/fail status, and a human-readable message.
type Check struct {
	Name     string `json:"name"`
	Severity string `json:"severity"`
	OK       bool   `json:"ok"`
	Message  string `json:"message"`
}

// Run executes all voom doctor checks and returns their results; when all is
// true, optional checks (such as virtiofsd) are included regardless of current
// state usage.
func Run(all bool) []Check {
	p := host.ResolvePaths()
	checks := []Check{}
	add := func(name string, fatal bool, err error, okmsg string) {
		if err != nil {
			sev := "warn"
			if fatal {
				sev = "fatal"
			}
			checks = append(checks, Check{name, sev, false, err.Error()})
		} else {
			checks = append(checks, Check{name, "ok", true, okmsg})
		}
	}
	add("host", true, host.ValidateHostImage(host.System(), state.DiskFormat(state.DefaultDriver()), state.DefaultDriver()), runtime.GOOS+"/"+runtime.GOARCH)
	if runtime.GOOS == "linux" {
		add("kvm", true, KVMAvailable(), "available")
		add("qemu", true, host.RequireExe("qemu-system-"+strings.TrimSuffix(host.System(), "-linux")), "found")
	}
	add("qemu-img", runtime.GOOS == "linux", host.RequireExe("qemu-img"), "found")
	if runtime.GOOS == "darwin" {
		add("vfkit", true, host.RequireExe("vfkit"), "found")
	}
	add("gvproxy", true, host.RequireExe("gvproxy"), "found")
	add("ssh", true, host.RequireExe("ssh"), "found")
	add("nix", false, host.RequireExe("nix"), "found")
	add("nixos-rebuild", false, host.RequireExe("nixos-rebuild"), "found")
	for _, d := range []string{p.State, p.Cache, p.Runtime} {
		var err error
		if d == p.Runtime {
			err = host.EnsureRuntimeDir(d)
		} else {
			err = os.MkdirAll(d, 0o755)
		}
		if err == nil {
			f, e := os.CreateTemp(d, ".write-test-*")
			err = e
			if e == nil {
				_ = f.Close()
				_ = os.Remove(f.Name())
			}
		}
		add("writable "+d, true, err, "writable")
	}
	st, err := state.Open()
	if err != nil {
		add("state", true, err, "")
		return checks
	}
	if _, err := st.AllocateSSHPort(); err != nil {
		add("ssh-port-range", true, err, "")
	} else {
		add("ssh-port-range", false, nil, "available")
	}
	if runtime.GOOS == "linux" && (all || hasShares(st) || hasControlShareImages(st)) {
		add("virtiofsd", false, host.RequireExe("virtiofsd"), "found")
	}
	checks = append(checks, StateDiagnostics(st)...)
	return checks
}

// KVMAvailable reports whether /dev/kvm is present and usable on the host,
// returning a descriptive error when it is not.
func KVMAvailable() error {
	return host.KVMAvailable()
}

// StateDiagnostics inspects the on-disk state store for dangling, duplicated,
// or orphaned VM and image entries, stale pidfiles, and other consistency
// issues, returning one Check per finding (or a single ok entry when clean).
func StateDiagnostics(st *state.Store) []Check {
	out := []Check{}
	vmMgr := vm.New(st)
	index := st.IndexSnapshot()
	vmSeenIDs := map[string]string{}
	for name, id := range index.VMs {
		vmRec, err := st.LoadVM(name)
		if err != nil || vmRec.ID != id {
			out = append(out, Check{"state-vm-" + name, "warn", false, "dangling VM index entry"})
			continue
		}
		if vmRec.Name != name || vmRec.ID != id {
			out = append(out, Check{"state-vm-mismatch-" + name, "warn", false, "VM object does not match state index entry"})
		}
		if prev := vmSeenIDs[vmRec.ID]; prev != "" {
			out = append(out, Check{"state-vm-duplicate-id", "warn", false, prev + " and " + name})
		}
		vmSeenIDs[vmRec.ID] = name
		if vmRec.Network.SSHBind == "0.0.0.0" || vmRec.Network.SSHBind == "::" {
			out = append(out, Check{"lan-ssh-" + name, "warn", false, "VM SSH is exposed beyond loopback"})
		}
		for _, f := range vmRec.Network.Forwards {
			if f.Bind == "0.0.0.0" || f.Bind == "::" {
				out = append(out, Check{"lan-forward-" + name + "-" + strconv.Itoa(f.HostPort), "warn", false, "VM forward is exposed beyond loopback"})
			}
		}
		rt := st.Runtime(vmRec)
		staticPidfiles := []struct {
			name string
			path string
			kind string
		}{{"vm.pid", rt.VMPid(), state.VMProcessKind(vmRec.Driver)}, {"gvproxy.pid", rt.GVProxyPid(), "gvproxy"}, {"auto-forward.pid", rt.AutoForwardPid(), "auto-forward"}}
		for _, pf := range staticPidfiles {
			if fileExists(pf.path) {
				if _, ok := process.ValidPID(pf.path, pf.kind); !ok {
					out = append(out, Check{"stale-pidfile-" + name + "-" + pf.name, "warn", false, "stale or mismatched pidfile at " + pf.path})
				}
			}
		}
		autoStatePath := vmMgr.RuntimeAutoForwardsPath(vmRec)
		if fileExists(autoStatePath) && !vmMgr.IsRunning(vmRec) {
			out = append(out, Check{"stale-auto-forward-state-" + name, "warn", false, "runtime auto-forward state remains for stopped VM at " + autoStatePath})
		}
		if vmRec.Network.AutoForward {
			if im, err := st.LoadImageByID(vmRec.Image.ID); err == nil && im.Capabilities.ControlShare && im.Capabilities.GuestPortReport && vmMgr.IsRunning(vmRec) {
				if _, err := vmMgr.ReadGuestPorts(vmRec); err != nil {
					out = append(out, Check{"auto-forward-report-" + name, "warn", false, err.Error()})
				}
			}
		}
		for _, path := range globFiles(rt.VirtiofsPidGlob()) {
			if _, ok := process.ValidPID(path, "virtiofsd"); !ok {
				pfName := filepath.Base(path)
				out = append(out, Check{"stale-pidfile-" + name + "-" + pfName, "warn", false, "stale or mismatched pidfile at " + path})
			}
		}
	}
	imageSeenIDs := map[string]string{}
	for name, id := range index.Images {
		im, err := st.LoadImage(name)
		if err != nil || im.ID != id {
			out = append(out, Check{"state-image-" + name, "warn", false, "dangling image index entry"})
			continue
		}
		if im.Name != name || im.ID != id {
			out = append(out, Check{"state-image-mismatch-" + name, "warn", false, "image object does not match state index entry"})
		}
		if prev := imageSeenIDs[im.ID]; prev != "" {
			out = append(out, Check{"state-image-duplicate-id", "warn", false, prev + " and " + name})
		}
		imageSeenIDs[im.ID] = name
	}
	if err := filepath.WalkDir(filepath.Join(st.Paths().State, "vms"), func(path string, d os.DirEntry, err error) error {
		if err == nil && d.Name() == "vm.json" {
			var vmRec state.VMRecord
			if state.ReadJSON(path, &vmRec) == nil {
				if index.VMs[vmRec.Name] == "" {
					out = append(out, Check{"state-vm-orphan-" + vmRec.Name, "warn", false, "orphaned VM object directory"})
				}
				if prev := vmSeenIDs[vmRec.ID]; prev != "" && prev != vmRec.Name {
					out = append(out, Check{"state-vm-duplicate-id", "warn", false, prev + " and " + vmRec.Name})
				}
				vmSeenIDs[vmRec.ID] = vmRec.Name
			}
		}
		return nil
	}); err != nil {
		out = append(out, Check{"state-vm-walk", "warn", false, err.Error()})
	}
	seenVMNames := map[string]string{}
	if err := filepath.WalkDir(filepath.Join(st.Paths().State, "vms"), func(path string, d os.DirEntry, err error) error {
		if err == nil && d.Name() == "vm.json" {
			var vmRec state.VMRecord
			if state.ReadJSON(path, &vmRec) == nil {
				if prev := seenVMNames[vmRec.Name]; prev != "" && prev != vmRec.ID {
					out = append(out, Check{"state-vm-duplicate-name-" + vmRec.Name, "warn", false, prev + " and " + vmRec.ID})
				}
				seenVMNames[vmRec.Name] = vmRec.ID
			}
		}
		return nil
	}); err != nil {
		out = append(out, Check{"state-vm-walk", "warn", false, err.Error()})
	}
	if err := filepath.WalkDir(filepath.Join(st.Paths().State, "images"), func(path string, d os.DirEntry, err error) error {
		if err == nil && d.Name() == "image.json" {
			var im state.ImageRecord
			if state.ReadJSON(path, &im) == nil {
				if index.Images[im.Name] == "" {
					out = append(out, Check{"state-image-orphan-" + im.Name, "warn", false, "orphaned image object directory"})
				}
				if prev := imageSeenIDs[im.ID]; prev != "" && prev != im.Name {
					out = append(out, Check{"state-image-duplicate-id", "warn", false, prev + " and " + im.Name})
				}
				imageSeenIDs[im.ID] = im.Name
			}
		}
		return nil
	}); err != nil {
		out = append(out, Check{"state-image-walk", "warn", false, err.Error()})
	}
	seenImageNames := map[string]string{}
	if err := filepath.WalkDir(filepath.Join(st.Paths().State, "images"), func(path string, d os.DirEntry, err error) error {
		if err == nil && d.Name() == "image.json" {
			var im state.ImageRecord
			if state.ReadJSON(path, &im) == nil {
				if prev := seenImageNames[im.Name]; prev != "" && prev != im.ID {
					out = append(out, Check{"state-image-duplicate-name-" + im.Name, "warn", false, prev + " and " + im.ID})
				}
				seenImageNames[im.Name] = im.ID
			}
		}
		return nil
	}); err != nil {
		out = append(out, Check{"state-image-walk", "warn", false, err.Error()})
	}
	if len(out) == 0 {
		out = append(out, Check{"state", "ok", true, "state index and objects are consistent"})
	}
	return out
}

func hasShares(st *state.Store) bool {
	vms, err := st.ListVMs()
	if err != nil {
		return false
	}
	for _, vmRec := range vms {
		if len(vmRec.Shares) > 0 {
			return true
		}
	}
	return false
}

func hasControlShareImages(st *state.Store) bool {
	images, err := st.ListImages()
	if err != nil {
		return false
	}
	for _, im := range images {
		if im.Capabilities.ControlShare {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func globFiles(pattern string) []string {
	out, _ := filepath.Glob(pattern)
	return out
}
