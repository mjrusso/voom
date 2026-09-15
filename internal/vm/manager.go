// Package vm orchestrates the lifecycle of voom-managed virtual machines,
// driving QEMU on Linux and vfkit on macOS.
package vm

import (
	"github.com/mjrusso/voom/internal/events"
	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

// Manager coordinates VM lifecycle, networking, shares, and guest interaction
// against a backing state store.
type Manager struct {
	store             *state.Store
	events            EventEmitter
	startSelfRecorded func([]string, string, string) error
}

// EventEmitter accepts best-effort change notifications.
type EventEmitter interface {
	Emit(events.Event)
}

type noopEmitter struct{}

func (noopEmitter) Emit(events.Event) {}

// Option customizes a Manager.
type Option func(*Manager)

// WithEventEmitter sends observed changes to emitter.
func WithEventEmitter(emitter EventEmitter) Option {
	return func(m *Manager) {
		if emitter != nil {
			m.events = emitter
		}
	}
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
func New(store *state.Store, opts ...Option) *Manager {
	m := &Manager{
		store:             store,
		events:            noopEmitter{},
		startSelfRecorded: process.StartSelfRecorded,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}
