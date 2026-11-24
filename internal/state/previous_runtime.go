package state

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/mjrusso/voom/internal/host"
)

var vfkitClientSocketName = regexp.MustCompile(`^vfkit-[0-9a-f]+-[0-9a-f]+\.sock$`)

// CheckPreviousRuntimes blocks VM startup and port allocation when the
// previous runtime may still be active.
func CheckPreviousRuntimes(root string) error {
	if root == "" {
		return nil
	}
	if owned, err := ownedRuntimeDirs(root); err != nil || !owned {
		return err
	}
	dir := filepath.Join(root, "vms")
	entries, err := readPreviousRuntime(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		artifact, err := previousRuntimeArtifact(filepath.Join(dir, entry.Name()))
		if err != nil {
			return err
		}
		if artifact != "" {
			return fmt.Errorf("file %q remains in the old runtime directory. Run VOOM_RUNTIME_DIR=%s voom list to check for VMs still running there", artifact, root)
		}
	}
	return nil
}

// CheckPreviousVMRuntime treats old runtime files as evidence that the VM
// or its helpers may still be running.
func CheckPreviousVMRuntime(root string, vm *VMRecord) error {
	if root == "" || vm.Driver != "vfkit" {
		return nil
	}
	dir := RuntimeAt(root, vm).Dir()
	if owned, err := ownedRuntimeDirs(root, filepath.Dir(dir)); err != nil || !owned {
		return err
	}
	artifact, err := previousRuntimeArtifact(dir)
	if err != nil || artifact == "" {
		return err
	}
	return fmt.Errorf("old runtime file %q remains for VM %q. To stop the VM and its helpers in the old runtime, run VOOM_RUNTIME_DIR=%s voom stop %s", artifact, vm.Name, root, vm.Name)
}

// RuntimeEFIStore finds the old EFI store if the user owns its runtime directories.
func RuntimeEFIStore(root string, vm *VMRecord) (string, error) {
	if root == "" {
		return "", nil
	}
	dir := RuntimeAt(root, vm).Dir()
	if owned, err := ownedRuntimeDirs(root, filepath.Dir(dir), dir); err != nil || !owned {
		return "", err
	}
	path := filepath.Join(dir, "efi-variable-store")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("EFI variable store %s must be a regular file", path)
	}
	return path, nil
}

func previousRuntimeArtifact(dir string) (string, error) {
	entries, err := readPreviousRuntime(dir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		name := entry.Name()
		switch name {
		case "vm.process.json", "gvproxy.process.json", "runtime-watch.process.json",
			"auto-forward.process.json", "vfkit.sock", "network.sock",
			"forward-mutation", "auto-forwards.json":
			return filepath.Join(dir, name), nil
		}
		if strings.HasPrefix(name, "vfkit-") && strings.HasSuffix(name, ".sock") {
			path := filepath.Join(dir, name)
			if !vfkitClientSocketName.MatchString(name) || entry.Type()&os.ModeSocket == 0 || !vfkitClientSocketGone(path) {
				return path, nil
			}
		}
	}
	return "", nil
}

// vfkit can leave its per-process datagram pathname behind when it exits.
func vfkitClientSocketGone(path string) bool {
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, os.ErrNotExist)
	}
	_ = conn.Close()
	return false
}

func readPreviousRuntime(dir string) ([]os.DirEntry, error) {
	if owned, err := ownedRuntimeDirs(dir); err != nil || !owned {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot inspect the previous runtime at %s: %w. Check its permissions and confirm old sessions are stopped before retrying", dir, err)
	}
	return entries, nil
}

func ownedRuntimeDirs(dirs ...string) (bool, error) {
	for _, dir := range dirs {
		owned, err := host.OwnsRuntimeDir(dir)
		if err != nil {
			return false, fmt.Errorf("cannot inspect the previous runtime at %s: %w. Check its permissions and confirm old sessions are stopped before retrying", dir, err)
		}
		if !owned {
			return false, nil
		}
	}
	return true, nil
}
