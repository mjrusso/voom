package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mjrusso/voom/internal/host"
)

// CopyVM copies stopped macOS storage and assigns a fresh Apple identity to the copy.
func (p *MacOSPlatform) CopyVM(ctx context.Context, src, dst string) (*MacOSPlatform, error) {
	if err := host.RequireExe("voom-macos-identity"); err != nil {
		return nil, err
	}
	unlock, err := lockMacOSStorage(src)
	if err != nil {
		return nil, fmt.Errorf("source macOS storage is in use: %w", err)
	}
	defer unlock()
	if err := p.ValidateStorage(src); err != nil {
		return nil, err
	}
	for _, name := range macOSStorageFiles {
		if err := CopyFile(ctx, filepath.Join(src, name), filepath.Join(dst, name)); err != nil {
			return nil, err
		}
	}
	return p.FreshIdentity(ctx, dst)
}

// FreshIdentity replaces only the copied MachineIdentifier; disk, hardware model and NVRAM stay paired.
func (p *MacOSPlatform) FreshIdentity(ctx context.Context, dir string) (*MacOSPlatform, error) {
	if err := p.ValidateStorage(dir); err != nil {
		return nil, err
	}
	helper, err := host.ExePath("voom-macos-identity")
	if err != nil {
		return nil, fmt.Errorf("macOS identity helper is missing; install the complete Voom package: %w", err)
	}
	output, err := exec.CommandContext(ctx, helper, filepath.Join(dir, "MachineIdentifier")).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("generating macOS identity: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := regularBundleFile(dir, "MachineIdentifier"); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "MachineIdentifier"))
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	fresh := *p
	fresh.IdentitySHA256 = hex.EncodeToString(hash[:])
	if fresh.IdentitySHA256 == p.IdentitySHA256 {
		return nil, errors.New("macOS identity helper retained the source identity")
	}
	if err := fresh.ValidateStorage(dir); err != nil {
		return nil, err
	}
	if err := syncPath(filepath.Join(dir, "MachineIdentifier")); err != nil {
		return nil, err
	}
	if err := syncDir(dir); err != nil {
		return nil, err
	}
	return &fresh, nil
}
