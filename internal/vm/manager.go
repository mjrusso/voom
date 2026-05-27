// Package vm orchestrates the lifecycle of voom-managed virtual machines,
// driving QEMU on Linux and vfkit on macOS.
package vm

import (
	"os/exec"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

// Manager coordinates VM lifecycle, networking, shares, and guest interaction
// against a backing state store.
type Manager struct {
	store             *state.Store
	startDetached     func(string, []string, string) (*exec.Cmd, error)
	startSelfDetached func([]string, string) (*exec.Cmd, error)
}

// ForwardRow describes a single forward entry as displayed to users.
type ForwardRow = forward.Row

// RuntimeAutoForward is a persisted runtime row describing an auto-forwarded port.
type RuntimeAutoForward = forward.RuntimeAuto

// GuestPortsReport is the parsed guest-reported listener inventory.
type GuestPortsReport = forward.PortsReport

// GuestListener describes a single listening port reported by the guest.
type GuestListener = forward.Listener

// New returns a Manager backed by the given state store.
func New(store *state.Store) *Manager {
	return &Manager{
		store:             store,
		startDetached:     startDetached,
		startSelfDetached: process.StartSelfDetached,
	}
}
