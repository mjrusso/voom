package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mjrusso/voom/internal/host"
)

// MacOSCloneProbe describes an unbooted bundle prepared for an identity experiment.
type MacOSCloneProbe struct {
	SchemaVersion          int       `json:"schemaVersion"`
	CreatedAt              time.Time `json:"createdAt"`
	Source                 string    `json:"source"`
	Destination            string    `json:"destination"`
	FreshIdentity          bool      `json:"freshIdentity"`
	SourceIdentitySHA256   string    `json:"sourceIdentitySha256"`
	CloneIdentitySHA256    string    `json:"cloneIdentitySha256"`
	HardwareModelSHA256    string    `json:"hardwareModelSha256"`
	AuxiliaryStorageSHA256 string    `json:"auxiliaryStorageSha256"`
	DiskBytes              int64     `json:"diskBytes"`
}

// PrepareMacOSCloneProbe copies a builder bundle into a new directory without registering or booting it.
func PrepareMacOSCloneProbe(ctx context.Context, src, dst string, fresh bool) (*MacOSCloneProbe, error) {
	bundle, err := readMacOSBundle(src)
	if err != nil {
		return nil, err
	}
	platform := bundle.platform
	if fresh {
		if err := host.ValidateHostImage("aarch64-darwin", "raw", "vfkit"); err != nil {
			return nil, err
		}
		if err := host.RequireExe("voom-macos-identity"); err != nil {
			return nil, err
		}
	}
	source, err := filepath.EvalSymlinks(src)
	if err != nil {
		return nil, err
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return nil, err
	}
	destination, err := filepath.Abs(dst)
	if err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return nil, err
	}
	destination = filepath.Join(parent, filepath.Base(destination))
	rel, err := filepath.Rel(source, destination)
	if err != nil {
		return nil, err
	}
	if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errors.New("probe destination must be outside the source bundle")
	}
	unlock, err := lockMacOSStorage(source)
	if err != nil {
		return nil, fmt.Errorf("source storage must be stopped and unlocked: %w", err)
	}
	defer unlock()
	if err := os.Mkdir(destination, 0o700); err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(destination)
		}
	}()
	for _, name := range macOSBundleFiles() {
		if err := CopyFile(ctx, filepath.Join(source, name), filepath.Join(destination, name)); err != nil {
			return nil, err
		}
		before, err := os.Stat(filepath.Join(source, name))
		if err != nil {
			return nil, err
		}
		after, err := os.Stat(filepath.Join(destination, name))
		if err != nil {
			return nil, err
		}
		if os.SameFile(before, after) || before.Size() != after.Size() {
			return nil, fmt.Errorf("probe %s is not an independent complete copy", name)
		}
	}
	if err := platform.ValidateStorage(destination); err != nil {
		return nil, err
	}
	if fresh {
		if _, err := platform.FreshIdentity(ctx, destination); err != nil {
			return nil, err
		}
	}
	// Disk/NVRAM locks do not protect source metadata from concurrent edits.
	cloned, err := readMacOSBundle(destination)
	if err != nil {
		return nil, err
	}
	identityChanged := platform.IdentitySHA256 != cloned.platform.IdentitySHA256
	if identityChanged != fresh {
		return nil, errors.New("probe machine identity does not match the requested experiment")
	}
	hashes := map[string]string{}
	for _, name := range []string{"HardwareModel", "AuxiliaryStorage"} {
		before, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			return nil, err
		}
		after, err := os.ReadFile(filepath.Join(destination, name))
		if err != nil {
			return nil, err
		}
		originalHash := sha256.Sum256(before)
		cloneHash := sha256.Sum256(after)
		if originalHash != cloneHash {
			return nil, fmt.Errorf("probe changed %s", name)
		}
		hashes[name] = hex.EncodeToString(originalHash[:])
	}
	info, err := os.Stat(filepath.Join(destination, "disk.raw"))
	if err != nil {
		return nil, err
	}
	report := &MacOSCloneProbe{SchemaVersion: 1, CreatedAt: time.Now().UTC(), Source: source, Destination: destination, FreshIdentity: fresh, SourceIdentitySHA256: platform.IdentitySHA256, CloneIdentitySHA256: cloned.platform.IdentitySHA256, HardwareModelSHA256: hashes["HardwareModel"], AuxiliaryStorageSHA256: hashes["AuxiliaryStorage"], DiskBytes: info.Size()}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := WriteJSONAtomic(filepath.Join(destination, "identity-probe.json"), report); err != nil {
		return nil, err
	}
	complete = true
	return report, nil
}
