package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mjrusso/voom/internal/egress"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/share"
	"github.com/mjrusso/voom/internal/state"
)

// AddShare attaches a host directory share to the VM under the given tag;
// the VM must be stopped.
func (m *Manager) AddShare(ctx context.Context, name, tag, hostPath, guestPath string, readonly bool) (share.Decl, error) {
	decl, err := share.NewDecl(tag, hostPath, guestPath, state.ControlShareTag, readonly)
	if err != nil {
		return share.Decl{}, err
	}
	lock, err := m.lockVM(ctx, name)
	if err != nil {
		return share.Decl{}, err
	}
	defer lock.Release()
	vm := lock.VM
	if m.IsRunning(vm) {
		return share.Decl{}, fmt.Errorf("VM %q is running; stop or restart the VM before changing shares", vm.Name)
	}
	for _, sh := range vm.Shares {
		if sh.Tag == tag {
			return share.Decl{}, fmt.Errorf("share tag %q already exists", tag)
		}
	}
	vm.Shares = append(vm.Shares, decl)
	vm.UpdatedAt = time.Now().UTC()
	return decl, m.store.SaveVM(vm)
}

// RemoveShare detaches the share with the given tag from the VM; the VM must
// be stopped.
func (m *Manager) RemoveShare(ctx context.Context, name, tag string) error {
	lock, err := m.lockVM(ctx, name)
	if err != nil {
		return err
	}
	defer lock.Release()
	vm := lock.VM
	if m.IsRunning(vm) {
		return fmt.Errorf("VM %q is running; stop or restart the VM before changing shares", vm.Name)
	}
	next := vm.Shares[:0]
	found := false
	for _, sh := range vm.Shares {
		if sh.Tag == tag {
			found = true
			continue
		}
		next = append(next, sh)
	}
	if !found {
		return fmt.Errorf("no share %q for VM %q", tag, name)
	}
	vm.Shares = next
	vm.UpdatedAt = time.Now().UTC()
	return m.store.SaveVM(vm)
}

// WriteControlFiles writes the control-share metadata and mounts manifest the
// guest reads to discover its identity and configured shares.
func (m *Manager) WriteControlFiles(vm *state.VMRecord) error {
	controlDir := m.store.ControlShareDir(vm)
	if err := os.MkdirAll(controlDir, 0o755); err != nil {
		return err
	}
	payload := map[string]any{
		"hostname": hostnameFor(vm.Name),
		"controlShare": map[string]any{
			"tag":        state.ControlShareTag,
			"guestPath":  state.ControlShareGuestPath(),
			"egressPath": egress.GuestManifest,
			"portsPath":  filepath.Join(state.ControlShareGuestPath(), "ports.json"),
			"mountsPath": filepath.Join(state.ControlShareGuestPath(), "mounts.json"),
		},
	}
	if err := state.WriteJSONAtomic(filepath.Join(controlDir, "metadata.json"), payload); err != nil {
		return err
	}
	mounts := make([]map[string]any, 0, len(vm.Shares))
	for _, sh := range vm.Shares {
		mounts = append(mounts, map[string]any{
			"tag":       sh.Tag,
			"guestPath": sh.GuestPath,
			"readonly":  sh.Readonly,
		})
	}
	return state.WriteJSONAtomic(m.store.ControlShareMountsPath(vm), mounts)
}

// StartControlShare starts (on QEMU) or describes (on vfkit) the reserved
// voom-control virtiofs share used for host-guest coordination.
func (m *Manager) StartControlShare(ctx context.Context, vm *state.VMRecord) (share.Runtime, error) {
	if vm.Driver == "vfkit" {
		return share.Runtime{
			Tag:      state.ControlShareTag,
			HostPath: m.store.ControlShareDir(vm),
			LogKind:  "virtiofs-" + state.ControlShareTag,
		}, nil
	}
	rt, err := share.ControlRuntime(m.store.Runtime(vm).Dir(), m.store.ControlShareDir(vm), state.ControlShareTag)
	if err != nil {
		return share.Runtime{}, err
	}
	return m.startVirtiofsd(ctx, vm, rt)
}

// StartUserShares launches virtiofsd for each user-declared share (QEMU) or
// returns runtime descriptors passed inline to the driver (vfkit).
func (m *Manager) StartUserShares(ctx context.Context, vm *state.VMRecord) ([]share.Runtime, error) {
	out := make([]share.Runtime, 0, len(vm.Shares))
	for _, sh := range vm.Shares {
		rt, err := m.userShareRuntime(vm, sh)
		if err != nil {
			return nil, errors.Join(err, m.stopVirtiofsd(m.store.Runtime(vm)))
		}
		if vm.Driver == "vfkit" {
			out = append(out, rt)
			continue
		}
		started, err := m.startVirtiofsd(ctx, vm, rt)
		if err != nil {
			return nil, errors.Join(err, m.stopVirtiofsd(m.store.Runtime(vm)))
		}
		out = append(out, started)
	}
	return out, nil
}

func (m *Manager) userShareRuntime(vm *state.VMRecord, sh share.Decl) (share.Runtime, error) {
	if vm.Driver == "vfkit" {
		return share.Runtime{
			Tag:       sh.Tag,
			HostPath:  sh.HostPath,
			GuestPath: sh.GuestPath,
			Readonly:  sh.Readonly,
			LogKind:   "virtiofs-" + sh.Tag,
		}, nil
	}
	return share.RuntimeForVM(m.store.Runtime(vm).Dir(), sh)
}

func (m *Manager) startVirtiofsd(ctx context.Context, vm *state.VMRecord, sh share.Runtime) (share.Runtime, error) {
	if err := m.startRecorded("virtiofsd", share.VirtiofsdArgs(sh), m.store.LogPath(vm, "share-mount"), sh.ProcessRecord); err != nil {
		return sh, err
	}
	if err := waitFor(ctx, func() bool {
		_, running := process.ValidRecord(sh.ProcessRecord, "virtiofsd")
		return running && socketExists(sh.Sock)
	}, virtiofsdStartTimeout); err != nil {
		return sh, fmt.Errorf("virtiofsd did not create socket %s", sh.Sock)
	}
	return sh, nil
}

func (m *Manager) stopVirtiofsd(rt state.RuntimeLayout) error {
	var failures []error
	for _, path := range globFiles(rt.VirtiofsSockGlob()) {
		recordPath := strings.TrimSuffix(path, ".sock") + ".process.json"
		if socketExists(path) && !process.HasRecord(recordPath) {
			failures = append(failures, fmt.Errorf("virtiofsd termination unconfirmed: %s has no process identity", path))
		}
	}
	for _, path := range process.FindRecords(rt.VirtiofsProcessRecordGlob()) {
		if err := process.StopRecorded(path, "virtiofsd", virtiofsdShutdownTimeout); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) == 0 {
		paths := append(globFiles(rt.VirtiofsSockGlob()), globFiles(rt.VirtiofsdLockFileGlob())...)
		for _, path := range paths {
			_ = os.Remove(path)
		}
	}
	return errors.Join(failures...)
}
