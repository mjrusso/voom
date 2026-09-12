// Package vm orchestrates the lifecycle of voom-managed virtual machines,
// driving QEMU on Linux and vfkit on macOS.
package vm

import (
	"context"
	"sync"
	"time"

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

type vmLock struct {
	VM            *state.VMRecord
	releaseGlobal func()
	releaseLocal  func()
}

func (l *vmLock) ReleaseGlobal() {
	l.releaseGlobal()
}

func (l *vmLock) Release() {
	l.releaseLocal()
	l.releaseGlobal()
}

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

// Callers must not hold either lock. Release global before slow VM work when no
// assignment or index mutation remains; never reacquire it while holding local.
func (m *Manager) lockVMState(ctx context.Context, name string) (*vmLock, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		global, err := m.store.LockGlobalAndReload()
		if err != nil {
			return nil, err
		}
		vm, err := m.store.LoadVM(name)
		if err != nil {
			global()
			return nil, err
		}
		local, err := m.store.TryLockVM(vm.ID)
		if err != nil {
			global()
			return nil, err
		}
		if local != nil {
			vm, err = m.store.LoadVMByID(vm.ID)
			if err != nil {
				local()
				global()
				return nil, err
			}
			return &vmLock{VM: vm, releaseGlobal: sync.OnceFunc(global), releaseLocal: sync.OnceFunc(local)}, nil
		}
		// Do not block unrelated VMs behind a long-running per-VM operation.
		global()
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (m *Manager) lockVM(ctx context.Context, name string) (*vmLock, error) {
	vm, release, err := m.store.LockVMRecord(ctx, name)
	if err != nil {
		return nil, err
	}
	return &vmLock{VM: vm, releaseGlobal: func() {}, releaseLocal: release}, nil
}
