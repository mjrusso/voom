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

// MacOSPlatform records the baked resources and Apple identity retained by an experimental guest.
type MacOSPlatform struct {
	IdentitySHA256 string `json:"identitySha256"`
	CPUs           int    `json:"cpus"`
	MemoryMiB      int    `json:"memoryMiB"`
	Version        string `json:"version"`
	Build          string `json:"build"`
}

// ValidateResources keeps allocations at or above the configuration validated by the bake.
// The bundle does not yet export Apple's minimum resource requirements.
func (p *MacOSPlatform) ValidateResources(cpus, memoryMiB int) error {
	if err := p.validate(); err != nil {
		return err
	}
	if cpus < p.CPUs || memoryMiB < p.MemoryMiB {
		return fmt.Errorf("macOS guest requires at least the baked allocation: %d CPUs and %dMiB", p.CPUs, p.MemoryMiB)
	}
	return nil
}

func (p *MacOSPlatform) validate() error {
	identity, err := hex.DecodeString(p.IdentitySHA256)
	if err != nil || len(identity) != sha256.Size || p.CPUs < 1 || p.MemoryMiB < 1 {
		return errors.New("invalid macOS platform metadata")
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
	for _, name := range append([]string{"disk.raw"}, macOSPlatformFiles...) {
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

var macOSPlatformFiles = []string{"MachineIdentifier", "HardwareModel", "AuxiliaryStorage"}

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
	if !opts.ExperimentalMacOS {
		return nil, errors.New("macOS bundle support is experimental; pass --experimental-macos to import this bundle")
	}
	if opts.MetaPath != "" || opts.InstallGuestHelpers || (opts.Arch != "" && opts.Arch != "aarch64-darwin") || (opts.Format != "" && opts.Format != "macos-bundle") {
		return nil, errors.New("macOS bundles use their own image.json; --meta, guest helpers, and conflicting architecture/format overrides are unsupported")
	}
	platform, user, err := readMacOSBundle(opts.Src)
	if err != nil {
		return nil, err
	}
	if opts.SSHUser != "" {
		user = opts.SSHUser
	}
	if user == "" {
		return nil, errors.New("macOS bundle must declare a user or supply --ssh-user")
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
	im := &ImageRecord{
		SchemaVersion: macOSRecordVersion, ID: id, Name: opts.Name, Arch: "aarch64-darwin", Format: "raw", Disk: "disk.raw", CreatedAt: time.Now().UTC(),
		Metadata: ImageMetadata{Source: opts.Src, SSHUser: user, SSHIdentityPath: opts.SSHIdentityPath},
		MacOS:    platform,
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(s.ImageDir(id))
		}
	}()
	for _, name := range macOSBundleFiles() {
		dest := name
		if name == "image.json" {
			dest = "bundle.json"
		}
		if err := CopyFile(ctx, filepath.Join(opts.Src, name), filepath.Join(s.ImageDir(id), dest)); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := im.MacOS.ValidateStorage(s.ImageDir(id)); err != nil {
		return nil, err
	}
	if err := s.registerImage(im); err != nil {
		return nil, err
	}
	committed = true
	return im, nil
}

// CopyMacOSPlatform copies persistent Apple state alongside the VM disk.
func CopyMacOSPlatform(ctx context.Context, src, dst string) error {
	for _, name := range macOSPlatformFiles {
		if err := regularBundleFile(src, name); err != nil {
			return err
		}
		if err := CopyFile(ctx, filepath.Join(src, name), filepath.Join(dst, name)); err != nil {
			return err
		}
	}
	return nil
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
		if err := regularBundleFile(dir, name); err != nil {
			unlock()
			return nil, err
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
	return append([]string{"image.json", "restore.json", "toolchain.json", "disk.raw"}, macOSPlatformFiles...)
}

func readMacOSBundle(src string) (*MacOSPlatform, string, error) {
	info, err := os.Lstat(filepath.Clean(src))
	if err != nil {
		return nil, "", err
	}
	if !info.IsDir() {
		return nil, "", errors.New("macOS bundle must be a directory, not a symlink")
	}

	for _, name := range macOSBundleFiles() {
		if err := regularBundleFile(src, name); err != nil {
			return nil, "", err
		}
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
		return nil, "", err
	}
	if manifest.SchemaVersion != 1 || manifest.System != "aarch64-darwin" || manifest.Format != "macos-bundle" {
		return nil, "", errors.New("unsupported macOS bundle schema, system, or format")
	}
	if manifest.Capabilities != (ImageCapabilities{}) {
		return nil, "", errors.New("macOS bundles cannot enable Linux guest capabilities")
	}
	if manifest.CPUCount < 1 || manifest.MemoryMiB < 1 || manifest.DiskGiB < 1 {
		return nil, "", errors.New("macOS bundle must declare positive CPU, memory, and disk allocations")
	}
	var restore struct {
		Version string `json:"version"`
		Build   string `json:"build"`
	}
	if err := ReadJSON(filepath.Join(src, "restore.json"), &restore); err != nil {
		return nil, "", err
	}
	major, err := strconv.Atoi(strings.Split(restore.Version, ".")[0])
	if err != nil || major < 27 || restore.Build == "" {
		return nil, "", errors.New("macOS bundle must declare macOS 27 or later and a restore build")
	}
	var toolchain map[string]any
	if err := ReadJSON(filepath.Join(src, "toolchain.json"), &toolchain); err != nil {
		return nil, "", err
	}
	identity, err := os.ReadFile(filepath.Join(src, "MachineIdentifier"))
	if err != nil {
		return nil, "", err
	}
	hash := sha256.Sum256(identity)

	return &MacOSPlatform{IdentitySHA256: hex.EncodeToString(hash[:]), CPUs: manifest.CPUCount, MemoryMiB: manifest.MemoryMiB, Version: restore.Version, Build: restore.Build}, manifest.User, nil
}
