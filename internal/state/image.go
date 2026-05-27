package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mjrusso/voom/internal/image"
)

// ImportOptions configures a call to Store.ImportImage. Name and Src are required;
// MetaPath, Arch, Format, and SSHUser are filled from metadata.json when omitted.
type ImportOptions struct {
	Name                string
	Src                 string
	MetaPath            string
	Arch                string
	Format              string
	SSHUser             string
	InstallGuestHelpers bool
}

// ImportImage copies the disk at opts.Src into the store, parses optional metadata, and registers a new image by name.
// The copy honors ctx cancellation; on cancel the partially-written disk is removed and the image is not registered.
func (s *Store) ImportImage(ctx context.Context, opts ImportOptions) (*ImageRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name, src, metaPath := opts.Name, opts.Src, opts.MetaPath
	arch, format, sshUser := opts.Arch, opts.Format, opts.SSHUser
	if _, ok := s.index.Images[name]; ok {
		return nil, fmt.Errorf("image %q already exists", name)
	}
	if _, err := os.Stat(src); err != nil {
		return nil, err
	}
	meta := ImageMetadata{Source: "manual-import"}
	caps := ImageCapabilities{}
	if metaPath != "" {
		b, err := os.ReadFile(metaPath)
		if err != nil {
			return nil, err
		}
		var raw map[string]any
		if err := json.Unmarshal(b, &raw); err != nil {
			return nil, err
		}
		if arch == "" {
			arch, _ = raw["system"].(string)
		}
		if format == "" {
			format, _ = raw["format"].(string)
		}
		if v, _ := raw["flake_rev"].(string); v != "" {
			meta.FlakeRev = v
		}
		if v, _ := raw["baked_at"].(string); v != "" {
			meta.BakedAt = v
		}
		if v, _ := raw["user"].(string); v != "" {
			meta.SSHUser = v
		}
		if v, _ := raw["sshIdentityPath"].(string); v != "" {
			meta.SSHIdentityPath = v
		}
		if v, _ := raw["ssh_identity_path"].(string); v != "" {
			meta.SSHIdentityPath = v
		}
		if v, _ := raw["nixosTargetUser"].(string); v != "" {
			meta.NixosTargetUser = v
		}
		if v, _ := raw["nixos_target_user"].(string); v != "" {
			meta.NixosTargetUser = v
		}
		if v, _ := raw["kernelPath"].(string); v != "" {
			meta.KernelPath = v
		}
		if v, _ := raw["kernel_path"].(string); v != "" {
			meta.KernelPath = v
		}
		if v, _ := raw["initrdPath"].(string); v != "" {
			meta.InitrdPath = v
		}
		if v, _ := raw["initrd_path"].(string); v != "" {
			meta.InitrdPath = v
		}
		if v, _ := raw["initPath"].(string); v != "" {
			meta.InitPath = v
		}
		if v, _ := raw["init_path"].(string); v != "" {
			meta.InitPath = v
		}
		if meta.KernelPath == "" {
			meta.KernelPath = bootspecString(raw, "kernel")
		}
		if meta.InitrdPath == "" {
			meta.InitrdPath = bootspecString(raw, "initrd")
		}
		if meta.InitPath == "" {
			meta.InitPath = bootspecString(raw, "init")
		}
		meta.KernelCmdline = ParseKernelCmdline(raw)
		caps = ParseCapabilities(raw)
		if v, _ := raw["installGuestHelpers"].(bool); v {
			meta.InstallGuestHelpers = true
		}
		if v, _ := raw["install_guest_helpers"].(bool); v {
			meta.InstallGuestHelpers = true
		}
		meta.Source = metaPath
	}
	if format == "" {
		format = InferFormat(src)
	}
	if format == "qcow" {
		format = "qcow2"
	}
	if format != "qcow2" && format != "raw" {
		return nil, fmt.Errorf("unsupported image format %q", format)
	}
	if arch == "" {
		return nil, errors.New("--arch is required when metadata does not declare system")
	}
	if err := ValidateImageArch(arch); err != nil {
		return nil, err
	}
	if sshUser != "" {
		meta.SSHUser = sshUser
	}
	if meta.SSHUser == "" {
		return nil, errors.New("--ssh-user is required when metadata does not declare user")
	}
	if meta.NixosTargetUser == "" {
		meta.NixosTargetUser = meta.SSHUser
	}
	if opts.InstallGuestHelpers {
		meta.InstallGuestHelpers = true
	}
	if meta.InstallGuestHelpers {
		caps.ControlShare = true
		caps.GuestPortReport = true
		caps.GuestShareMount = true
	}
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	im := &ImageRecord{SchemaVersion: SchemaVersion, ID: id, Name: name, Arch: arch, Format: format, Disk: "disk." + format, CreatedAt: time.Now().UTC(), Metadata: meta, Capabilities: caps}
	if err := os.MkdirAll(s.ImageDir(id), 0o755); err != nil {
		return nil, err
	}
	if err := CopyFile(ctx, src, s.ImageDiskPath(im)); err != nil {
		return nil, err
	}
	if im.Format == "raw" && meta.KernelPath == "" {
		if boot, err := image.ExtractVFKitDirectBoot(s.ImageDiskPath(im), s.ImageDir(id)); err == nil {
			im.Metadata.KernelPath = filepath.Join(s.ImageDir(id), boot.KernelRelPath)
			im.Metadata.InitrdPath = filepath.Join(s.ImageDir(id), boot.InitrdRelPath)
			im.Metadata.InitPath = boot.InitPath
			im.Metadata.KernelCmdline = append([]string{}, boot.KernelCmdline...)
		}
	}
	if err := WriteJSONAtomic(filepath.Join(s.ImageDir(id), "image.json"), im); err != nil {
		return nil, err
	}
	s.index.Images[name] = id
	return im, s.saveIndexLocked()
}

// ParseCapabilities extracts ImageCapabilities flags from an image metadata map, checking a nested "capabilities" object when present.
func ParseCapabilities(raw map[string]any) ImageCapabilities {
	caps := ImageCapabilities{}
	values := raw
	if nested, ok := raw["capabilities"].(map[string]any); ok {
		values = nested
	}
	boolField := func(name string) bool {
		v, _ := values[name].(bool)
		return v
	}
	caps.MetadataDisk = boolField("metadataDisk")
	caps.ControlShare = boolField("controlShare")
	caps.GuestPortReport = boolField("guestPortReport")
	caps.GuestShareMount = boolField("guestShareMount")
	caps.NixosSwitch = boolField("nixosSwitch")
	return caps
}

// LoadImage resolves an image name through the index and loads its record from disk.
func (s *Store) LoadImage(name string) (*ImageRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadImageLocked(name)
}

func (s *Store) loadImageLocked(name string) (*ImageRecord, error) {
	id, ok := s.index.Images[name]
	if !ok {
		return nil, fmt.Errorf("no such image %q", name)
	}
	im, err := s.LoadImageByID(id)
	if err != nil {
		return nil, err
	}
	if im.Name != name || im.ID != id {
		return nil, fmt.Errorf("image index mismatch for %q", name)
	}
	return im, nil
}

// LoadImageByID loads an image record directly by its ID, bypassing the name index.
func (s *Store) LoadImageByID(id string) (*ImageRecord, error) {
	var im ImageRecord
	if err := ReadJSON(filepath.Join(s.ImageDir(id), "image.json"), &im); err != nil {
		return nil, err
	}
	if im.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("unsupported image schema version %d", im.SchemaVersion)
	}
	return &im, nil
}

// ListImages returns all image records in name order.
func (s *Store) ListImages() ([]*ImageRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := keys(s.index.Images)
	out := []*ImageRecord{}
	for _, n := range names {
		im, err := s.loadImageLocked(n)
		if err != nil {
			return nil, err
		}
		out = append(out, im)
	}
	return out, nil
}

// RemoveImage deletes an image from the store; if VMs still reference it, force must be true.
// It returns the list of referencing VM names regardless of outcome.
func (s *Store) RemoveImage(name string, force bool) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	im, err := s.loadImageLocked(name)
	if err != nil {
		return nil, err
	}
	refs := []string{}
	vms, _ := s.listVMsLocked()
	for _, vm := range vms {
		if vm.Image.ID == im.ID {
			refs = append(refs, vm.Name)
		}
	}
	if len(refs) > 0 && !force {
		return refs, errors.New("image is in use")
	}
	delete(s.index.Images, name)
	if err := s.saveIndexLocked(); err != nil {
		return refs, err
	}
	return refs, os.RemoveAll(s.ImageDir(im.ID))
}

func bootspecString(raw map[string]any, key string) string {
	if spec := bootspecMap(raw); spec != nil {
		if v, _ := spec[key].(string); v != "" {
			return v
		}
	}
	if v, _ := raw[key].(string); v != "" {
		return v
	}
	return ""
}

func bootspecMap(raw map[string]any) map[string]any {
	if spec, ok := raw["org.nixos.bootspec.v1"].(map[string]any); ok {
		return spec
	}
	return nil
}

// VFKitBootPaths returns the kernel path, initrd path, and joined kernel command line for direct-boot under vfkit.
func VFKitBootPaths(im *ImageRecord) (kernelPath, initrdPath, kernelCmd string) {
	if im == nil || im.Metadata.KernelPath == "" {
		return "", "", ""
	}
	params := append([]string{}, im.Metadata.KernelCmdline...)
	if im.Metadata.InitPath != "" && !containsKernelArg(params, "init=") {
		params = append(params, "init="+im.Metadata.InitPath)
	}
	return im.Metadata.KernelPath, im.Metadata.InitrdPath, strings.Join(params, " ")
}

func containsKernelArg(args []string, prefix string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return true
		}
	}
	return false
}
