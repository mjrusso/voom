package state

import (
	"fmt"
	"sync"
	"time"

	"github.com/mjrusso/voom/internal/egress"
	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/host"
	"github.com/mjrusso/voom/internal/pci"
	"github.com/mjrusso/voom/internal/share"
	"github.com/mjrusso/voom/internal/usb"
)

// Index is the top-level state.json document mapping VM and image names to their IDs.
type Index struct {
	SchemaVersion int               `json:"schemaVersion"`
	VMs           map[string]string `json:"vms"`
	Images        map[string]string `json:"images"`
}

// ImageRecord is the on-disk record describing an imported image and its boot metadata.
type ImageRecord struct {
	SchemaVersion int               `json:"schemaVersion"`
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Arch          string            `json:"arch"`
	Format        string            `json:"format"`
	Disk          string            `json:"disk"`
	CreatedAt     time.Time         `json:"createdAt"`
	MacOS         *MacOSPlatform    `json:"macos,omitempty"`
	Metadata      ImageMetadata     `json:"metadata"`
	Capabilities  ImageCapabilities `json:"capabilities"`
}

// ImageMetadata holds provenance, SSH access defaults, and direct-boot info for an image.
type ImageMetadata struct {
	Source              string   `json:"source"`
	FlakeRev            string   `json:"flakeRev"`
	BakedAt             string   `json:"bakedAt"`
	SSHUser             string   `json:"sshUser"`
	SSHIdentityPath     string   `json:"sshIdentityPath"`
	NixosTargetUser     string   `json:"nixosTargetUser"`
	KernelPath          string   `json:"kernelPath,omitempty"`
	InitrdPath          string   `json:"initrdPath,omitempty"`
	InitPath            string   `json:"initPath,omitempty"`
	KernelCmdline       []string `json:"kernelCmdline,omitempty"`
	InstallGuestHelpers bool     `json:"installGuestHelpers,omitempty"`
}

// ImageCapabilities flags features the guest in this image supports.
type ImageCapabilities struct {
	MetadataDisk    bool `json:"metadataDisk"`
	ControlShare    bool `json:"controlShare"`
	GuestPortReport bool `json:"guestPortReport"`
	GuestShareMount bool `json:"guestShareMount"`
	NixosSwitch     bool `json:"nixosSwitch"`
}

// VMRecord is the on-disk record describing a managed VM.
type VMRecord struct {
	SchemaVersion     int            `json:"schemaVersion"`
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	Driver            string         `json:"driver"`
	Arch              string         `json:"arch"`
	CreatedAt         time.Time      `json:"createdAt"`
	UpdatedAt         time.Time      `json:"updatedAt"`
	Image             VMImageRef     `json:"image"`
	MacOS             *MacOSPlatform `json:"macos,omitempty"`
	StorageGeneration string         `json:"storageGeneration,omitempty"`
	Resources         VMResources    `json:"resources"`
	Access            VMAccess       `json:"access"`
	Network           VMNetwork      `json:"network"`
	Shares            []share.Decl   `json:"shares"`
	USBDevices        []usb.Decl     `json:"usbDevices,omitempty"`
	PCIDevices        []pci.Decl     `json:"pciDevices,omitempty"`
	Nixos             *NixOSSwitch   `json:"nixos,omitempty"`
}

func (vm *VMRecord) validate() error {
	want := SchemaVersion
	if vm.MacOS != nil || vm.StorageGeneration != "" {
		want = storageRecordVersion
	}
	if vm.SchemaVersion != want {
		return fmt.Errorf("unsupported record schema version %d", vm.SchemaVersion)
	}
	if vm.MacOS != nil || vm.SchemaVersion == storageRecordVersion {
		if !validStorageGeneration(vm.StorageGeneration) {
			return fmt.Errorf("invalid storage generation")
		}
	}
	if vm.Arch == "aarch64-darwin" {
		if vm.Driver != "vfkit" || vm.MacOS == nil {
			return fmt.Errorf("macOS guests require vfkit and platform metadata")
		}
		if err := vm.MacOS.ValidateResources(vm.Resources.CPUs, vm.Resources.MemoryMiB); err != nil {
			return err
		}
		if len(vm.USBDevices) > 0 || len(vm.PCIDevices) > 0 || vm.Network.Egress != nil || vm.Nixos != nil {
			return fmt.Errorf("macOS guests do not support USB, PCI, egress, or NixOS switching")
		}
	} else if vm.MacOS != nil {
		return fmt.Errorf("macOS platform metadata requires an aarch64-darwin guest")
	}
	if err := usb.ValidateAssignments(vm.USBDevices); err != nil {
		return fmt.Errorf("VM %q: %w", vm.Name, err)
	}
	if err := pci.ValidateAssignments(vm.PCIDevices); err != nil {
		return fmt.Errorf("VM %q: %w", vm.Name, err)
	}
	return nil
}

// VMImageRef captures the image a VM was created from (ID is authoritative; Name is a hint).
type VMImageRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// VMResources describes the CPU and memory allocation for a VM.
type VMResources struct {
	CPUs      int `json:"cpus"`
	MemoryMiB int `json:"memoryMiB"`
}

// VMAccess captures SSH credentials and the target NixOS user for guest operations.
type VMAccess struct {
	SSHUser         string `json:"sshUser"`
	SSHIdentityPath string `json:"sshIdentityPath"`
	NixosTargetUser string `json:"nixosTargetUser"`
}

// VMNetwork declares SSH binding, port forwarding, and explicit proxy attachments.
type VMNetwork struct {
	Egress                *egress.Decl   `json:"egress,omitempty"`
	SSHPort               int            `json:"sshPort"`
	SSHBind               string         `json:"sshBind"`
	Forwards              []forward.Decl `json:"forwards"`
	AutoForward           bool           `json:"autoForward"`
	AutoForwardHostOffset int            `json:"autoForwardHostOffset"`
	AutoForwardBind       string         `json:"autoForwardBind,omitempty"`
}

// NixOSSwitch records the last successful nixos-rebuild switch performed against this VM.
type NixOSSwitch struct {
	SwitchedAt time.Time `json:"switchedAt"`
	FlakeRef   string    `json:"flakeRef"`
	FlakeRev   string    `json:"flakeRev"`
}

// Store is the entry point for reading and mutating voom's on-disk state.
// The in-memory index is guarded by mu; the on-disk view is guarded by the
// cross-process flocks acquired by the store.
type Store struct {
	paths             host.Paths
	mu                sync.Mutex
	index             Index
	hostPortAvailable HostPortAvailableFunc
}
