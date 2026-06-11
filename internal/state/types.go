package state

import (
	"sync"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/host"
	"github.com/mjrusso/voom/internal/share"
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
	SchemaVersion int          `json:"schemaVersion"`
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Driver        string       `json:"driver"`
	Arch          string       `json:"arch"`
	CreatedAt     time.Time    `json:"createdAt"`
	UpdatedAt     time.Time    `json:"updatedAt"`
	Image         VMImageRef   `json:"image"`
	Resources     VMResources  `json:"resources"`
	Access        VMAccess     `json:"access"`
	Network       VMNetwork    `json:"network"`
	Shares        []share.Decl `json:"shares"`
	Nixos         *NixOSSwitch `json:"nixos,omitempty"`
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

// VMNetwork describes the VM's SSH binding, declared forwards, and auto-forward policy.
type VMNetwork struct {
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
// cross-process flocks acquired via LockGlobal/LockVM.
type Store struct {
	paths             host.Paths
	mu                sync.Mutex
	index             Index
	hostPortAvailable HostPortAvailableFunc
}
