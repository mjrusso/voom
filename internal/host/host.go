// Package host provides host-system queries: paths (XDG-aware),
// runtime directory, target system identification, and executable resolution.
package host

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Paths holds the resolved per-user directories used by voom.
type Paths struct {
	Config  string `json:"config"`
	State   string `json:"state"`
	Cache   string `json:"cache"`
	Runtime string `json:"runtime"`
}

// ResolvePaths returns the per-user config, state, cache, and runtime directories, honoring VOOM_* and XDG_* environment overrides.
func ResolvePaths() Paths {
	// Empty home falls through to a relative path; callers surface the failure when they touch disk.
	home, _ := os.UserHomeDir()
	uid := os.Getuid()
	p := Paths{}
	if runtime.GOOS == "darwin" {
		base := filepath.Join(home, "Library", "Application Support", "voom")
		p.Config, p.State = base, base
		p.Cache = filepath.Join(home, "Library", "Caches", "voom")
		p.Runtime = filepath.Join(os.TempDir(), fmt.Sprintf("voom-%d", uid))
		p.Config = firstEnv("VOOM_CONFIG_DIR", p.Config)
		p.State = firstEnv("VOOM_STATE_DIR", p.State)
		p.Cache = firstEnv("VOOM_CACHE_DIR", p.Cache)
		p.Runtime = firstEnv("VOOM_RUNTIME_DIR", p.Runtime)
		return p
	}
	p.Config = firstEnv("VOOM_CONFIG_DIR", xdg("XDG_CONFIG_HOME", filepath.Join(home, ".config"), "voom"))
	p.State = firstEnv("VOOM_STATE_DIR", xdg("XDG_DATA_HOME", filepath.Join(home, ".local", "share"), "voom"))
	p.Cache = firstEnv("VOOM_CACHE_DIR", xdg("XDG_CACHE_HOME", filepath.Join(home, ".cache"), "voom"))
	p.Runtime = firstEnv("VOOM_RUNTIME_DIR", RuntimeDir(uid))
	return p
}

// RuntimeDir returns the voom runtime directory for uid, using XDG_RUNTIME_DIR when set and falling back to a per-uid path under the system temp directory.
func RuntimeDir(uid int) string {
	if base := os.Getenv("XDG_RUNTIME_DIR"); base != "" {
		return filepath.Join(base, "voom")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("voom-%d", uid))
}

// System returns the host's target system identifier (e.g. "x86_64-linux" or "aarch64-linux") derived from runtime.GOARCH.
func System() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64-linux"
	case "arm64":
		return "aarch64-linux"
	default:
		return runtime.GOARCH + "-linux"
	}
}

// ValidateHostImage verifies that an image's arch and disk format are compatible with the host and the requested driver (qemu or vfkit).
func ValidateHostImage(arch, format, driver string) error {
	hostArch := runtime.GOARCH
	if hostArch == "amd64" {
		hostArch = "x86_64"
	}
	if hostArch == "arm64" {
		hostArch = "aarch64"
	}
	want := hostArch + "-linux"
	if arch != want {
		return fmt.Errorf("cross-arch emulation is not supported: image %s on host %s", arch, want)
	}
	if driver == "qemu" {
		if format != "qcow2" {
			return fmt.Errorf("qemu requires qcow2 image/disk format, got %s", format)
		}
		if runtime.GOOS != "linux" {
			return errors.New("qemu driver is supported only on Linux")
		}
		if err := KVMAvailable(); err != nil {
			return err
		}
		return nil
	}
	if driver == "vfkit" {
		if runtime.GOOS != "darwin" {
			return errors.New("vfkit driver is supported only on macOS")
		}
		if runtime.GOARCH != "arm64" {
			return errors.New("vfkit driver currently supports Apple Silicon hosts only")
		}
		if format != "raw" {
			return fmt.Errorf("vfkit requires raw image/disk format, got %s", format)
		}
		return nil
	}
	return fmt.Errorf("unsupported driver %q", driver)
}

// KVMAvailable reports whether /dev/kvm exists and is a device node, returning an error otherwise.
func KVMAvailable() error {
	info, err := os.Stat("/dev/kvm")
	if err != nil {
		return errors.New("KVM not available at /dev/kvm")
	}
	if info.IsDir() {
		return errors.New("KVM path is not a device: /dev/kvm")
	}
	return nil
}

// RequireExe returns an error if the named executable cannot be resolved via ExePath.
func RequireExe(name string) error {
	_, err := ExePath(name)
	if err != nil {
		return fmt.Errorf("missing required command: %s", name)
	}
	return nil
}

// ExePath resolves the named executable, preferring the path in its VOOM_* override environment variable (see ExeOverrideName) before falling back to PATH lookup.
func ExePath(name string) (string, error) {
	if override := os.Getenv(ExeOverrideName(name)); override != "" {
		if info, err := os.Stat(override); err == nil && !info.IsDir() {
			return override, nil
		}
	}
	return exec.LookPath(name)
}

// ExeOverrideName returns the environment variable name used to override the path to the given executable (e.g. "gvproxy" -> "VOOM_GVPROXY").
func ExeOverrideName(name string) string {
	switch name {
	case "gvproxy":
		return "VOOM_GVPROXY"
	case "virtiofsd":
		return "VOOM_VIRTIOFSD"
	default:
		return "VOOM_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	}
}

func firstEnv(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func xdg(env, fallback, child string) string {
	base := os.Getenv(env)
	if base == "" {
		base = fallback
	}
	return filepath.Join(base, child)
}
