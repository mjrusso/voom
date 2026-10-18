package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// MacOSPlatform records a guest's Apple identity, macOS version, and resource limits.
type MacOSPlatform struct {
	IdentitySHA256   string `json:"identitySha256"`
	CPUs             int    `json:"cpus"`
	MemoryMiB        int    `json:"memoryMiB"`
	Version          string `json:"version"`
	Build            string `json:"build"`
	MinimumCPUs      int    `json:"minimumCPUs,omitempty"`
	MinimumMemoryMiB int    `json:"minimumMemoryMiB,omitempty"`
}

// ValidateResources enforces Apple's recorded minimums, or the baked allocation for legacy bundles.
func (p *MacOSPlatform) ValidateResources(cpus, memoryMiB int) error {
	if err := p.validate(); err != nil {
		return err
	}
	minimumCPUs, minimumMemory := p.CPUs, p.MemoryMiB
	if p.MinimumCPUs != 0 {
		minimumCPUs, minimumMemory = p.MinimumCPUs, p.MinimumMemoryMiB
	}
	if cpus < minimumCPUs || memoryMiB < minimumMemory {
		return fmt.Errorf("macOS guest requires at least %d CPUs and %dMiB", minimumCPUs, minimumMemory)
	}
	return nil
}

func (p *MacOSPlatform) validate() error {
	identity, err := hex.DecodeString(p.IdentitySHA256)
	if err != nil || len(identity) != sha256.Size || p.CPUs < 1 || p.MemoryMiB < 1 {
		return errors.New("invalid macOS platform metadata")
	}
	if p.MinimumCPUs < 0 || p.MinimumMemoryMiB < 0 ||
		(p.MinimumCPUs == 0) != (p.MinimumMemoryMiB == 0) ||
		p.MinimumCPUs > p.CPUs || p.MinimumMemoryMiB > p.MemoryMiB {
		return errors.New("invalid macOS resource minimums")
	}
	major, err := strconv.Atoi(strings.Split(p.Version, ".")[0])
	if err != nil || major < 27 || p.Build == "" {
		return errors.New("macOS platform metadata must declare macOS 27 or later and a restore build")
	}
	return nil
}

// ValidateStorage checks the files and recorded identity without reading the disk or NVRAM contents.
func (p *MacOSPlatform) ValidateStorage(dir string) error {
	if err := p.validate(); err != nil {
		return err
	}
	for _, name := range macOSStorageFiles {
		if err := regularBundleFile(dir, name); err != nil {
			return err
		}
	}
	identity, err := os.ReadFile(filepath.Join(dir, "MachineIdentifier"))
	if err != nil {
		return err
	}
	hash := sha256.Sum256(identity)
	if hex.EncodeToString(hash[:]) != p.IdentitySHA256 {
		return errors.New("macOS MachineIdentifier does not match the recorded Apple identity")
	}
	return nil
}

var macOSStorageFiles = []string{"disk.raw", "MachineIdentifier", "HardwareModel", "AuxiliaryStorage"}

const macOSRecordVersion = 2

func validateRecordVersion(version int, macOS bool) error {
	want := SchemaVersion
	if macOS {
		want = macOSRecordVersion
	}
	if version != want {
		return fmt.Errorf("unsupported record schema version %d", version)
	}
	return nil
}

func regularBundleFile(dir, name string) error {
	info, err := os.Lstat(filepath.Join(dir, name))
	if err != nil {
		return fmt.Errorf("macOS bundle %s: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("macOS bundle %s must be a nonempty regular file", name)
	}
	return nil
}

func (s *Store) importMacOSImage(ctx context.Context, opts ImportOptions) (*ImageRecord, error) {
	if opts.MetaPath != "" || opts.InstallGuestHelpers || (opts.Arch != "" && opts.Arch != "aarch64-darwin") || (opts.Format != "" && opts.Format != "macos-bundle") {
		return nil, errors.New("macOS bundles use their own image.json; --meta, guest helpers, and conflicting architecture/format overrides are unsupported")
	}
	if err := validateMacOSBundleFiles(opts.Src); err != nil {
		return nil, err
	}
	unlock, err := lockMacOSStorage(opts.Src)
	if err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("macOS bundle storage is in use; shut down the source VM before importing: %w", err)
		}
		return nil, err
	}
	defer unlock()
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(s.ImageDir(id))
		}
	}()
	for _, name := range macOSBundleFiles() {
		if err := CopyFile(ctx, filepath.Join(opts.Src, name), filepath.Join(s.ImageDir(id), name)); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bundle, err := readMacOSBundle(s.ImageDir(id))
	if err != nil {
		return nil, err
	}
	user := bundle.user
	if opts.SSHUser != "" {
		user = opts.SSHUser
	}
	if user == "" {
		return nil, errors.New("macOS bundle must declare a user or supply --ssh-user")
	}
	im := &ImageRecord{
		SchemaVersion: macOSRecordVersion, ID: id, Name: opts.Name, Arch: "aarch64-darwin", Format: "raw", Disk: "disk.raw", CreatedAt: time.Now().UTC(),
		Metadata:     ImageMetadata{Source: opts.Src, SSHUser: user, SSHIdentityPath: opts.SSHIdentityPath},
		MacOS:        bundle.platform,
		Capabilities: bundle.capabilities,
	}
	if err := os.Rename(filepath.Join(s.ImageDir(id), "image.json"), filepath.Join(s.ImageDir(id), "bundle.json")); err != nil {
		return nil, err
	}
	if err := s.registerImage(im); err != nil {
		return nil, err
	}
	committed = true
	return im, nil
}

// CheckMacOSIdentityAvailable requires the global lock through VM registration.
func (s *Store) CheckMacOSIdentityAvailable(platform *MacOSPlatform) error {
	vms, err := s.ListVMs()
	if err != nil {
		return err
	}
	for _, vm := range vms {
		if vm.MacOS != nil && vm.MacOS.IdentitySHA256 == platform.IdentitySHA256 {
			return fmt.Errorf("macOS identity is already assigned to VM %q", vm.Name)
		}
	}
	return nil
}

func lockMacOSStorage(dir string) (func(), error) {
	var files []*os.File
	unlock := func() {
		for _, f := range files {
			_ = f.Close()
		}
	}
	for _, name := range []string{"disk.raw", "AuxiliaryStorage"} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			// Stop/removal must still lock surviving components of an incomplete VM.
			// Boot and copy operations validate completeness separately.
			continue
		}
		if err != nil {
			unlock()
			return nil, err
		}
		if !info.Mode().IsRegular() {
			unlock()
			return nil, fmt.Errorf("macOS storage %s must be a regular file", name)
		}
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			unlock()
			return nil, err
		}
		files = append(files, f)
		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			unlock()
			return nil, err
		}
	}
	return unlock, nil
}

// WaitMacOSStorage waits for Virtualization.framework to release disk and NVRAM locks.
func WaitMacOSStorage(ctx context.Context, dir string) error {
	waitCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	for {
		if err := waitCtx.Err(); err != nil {
			return fmt.Errorf("waiting for macOS storage locks: %w", err)
		}
		unlock, err := lockMacOSStorage(dir)
		if err == nil {
			unlock()
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return err
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("waiting for macOS storage locks: %w", waitCtx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func macOSBundleFiles() []string {
	return append([]string{"image.json", "restore.json", "toolchain.json"}, macOSStorageFiles...)
}

type macOSBundleMetadata struct {
	platform     *MacOSPlatform
	user         string
	capabilities ImageCapabilities
}

func validateMacOSBundleFiles(src string) error {
	info, err := os.Lstat(filepath.Clean(src))
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("macOS bundle must be a directory, not a symlink")
	}

	for _, name := range macOSBundleFiles() {
		if err := regularBundleFile(src, name); err != nil {
			return err
		}
	}
	return nil
}

func readMacOSBundle(src string) (*macOSBundleMetadata, error) {
	if err := validateMacOSBundleFiles(src); err != nil {
		return nil, err
	}
	var manifest struct {
		SchemaVersion int               `json:"schemaVersion"`
		System        string            `json:"system"`
		Format        string            `json:"format"`
		User          string            `json:"user"`
		CPUCount      int               `json:"cpuCount"`
		MemoryMiB     int               `json:"memoryMiB"`
		DiskGiB       int               `json:"diskGiB"`
		Capabilities  ImageCapabilities `json:"capabilities"`
	}
	if err := ReadJSON(filepath.Join(src, "image.json"), &manifest); err != nil {
		return nil, err
	}
	if manifest.SchemaVersion != 1 || manifest.System != "aarch64-darwin" || manifest.Format != "macos-bundle" {
		return nil, errors.New("unsupported macOS bundle schema, system, or format")
	}
	if !validMacOSCapabilities(manifest.Capabilities) {
		return nil, errors.New("macOS bundles require controlShare for guest services and cannot enable metadataDisk or nixosSwitch")
	}
	if manifest.CPUCount < 1 || manifest.MemoryMiB < 1 || manifest.DiskGiB < 1 {
		return nil, errors.New("macOS bundle must declare positive CPU, memory, and disk allocations")
	}
	var restore struct {
		Version           string  `json:"version"`
		Build             string  `json:"build"`
		MinimumCPUCount   *int    `json:"minimumCPUCount"`
		MinimumMemorySize *uint64 `json:"minimumMemorySize"`
	}
	if err := ReadJSON(filepath.Join(src, "restore.json"), &restore); err != nil {
		return nil, err
	}
	var toolchain map[string]any
	if err := ReadJSON(filepath.Join(src, "toolchain.json"), &toolchain); err != nil {
		return nil, err
	}
	identity, err := os.ReadFile(filepath.Join(src, "MachineIdentifier"))
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(identity)

	platform := &MacOSPlatform{IdentitySHA256: hex.EncodeToString(hash[:]), CPUs: manifest.CPUCount, MemoryMiB: manifest.MemoryMiB, Version: restore.Version, Build: restore.Build}
	if (restore.MinimumCPUCount == nil) != (restore.MinimumMemorySize == nil) {
		return nil, errors.New("macOS restore metadata must declare both resource minimums")
	}
	if restore.MinimumCPUCount != nil {
		if *restore.MinimumCPUCount < 1 || *restore.MinimumMemorySize < 1 {
			return nil, errors.New("macOS resource minimums must be positive")
		}
		minimumMiB := (*restore.MinimumMemorySize-1)/(1024*1024) + 1
		if minimumMiB > uint64(manifest.MemoryMiB) {
			return nil, errors.New("macOS memory minimum exceeds the baked allocation")
		}
		platform.MinimumCPUs = *restore.MinimumCPUCount
		platform.MinimumMemoryMiB = int(minimumMiB)
	}
	if err := platform.validate(); err != nil {
		return nil, err
	}
	return &macOSBundleMetadata{platform: platform, user: manifest.User, capabilities: manifest.Capabilities}, nil
}

func validMacOSCapabilities(c ImageCapabilities) bool {
	return !c.MetadataDisk && !c.NixosSwitch && (c.ControlShare || (!c.GuestPortReport && !c.GuestShareMount))
}
