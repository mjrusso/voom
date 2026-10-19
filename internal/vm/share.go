package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	vm, unlock, err := m.store.LockVMRecord(ctx, name)
	if err != nil {
		return share.Decl{}, err
	}
	defer unlock()
	im, err := m.store.LoadImageByID(vm.Image.ID)
	if err != nil {
		return share.Decl{}, err
	}
	if err := requireShareMount(im); err != nil {
		return share.Decl{}, err
	}
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
	vm, unlock, err := m.store.LockVMRecord(ctx, name)
	if err != nil {
		return err
	}
	defer unlock()
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

// WriteControlFiles stages configuration locally; only legacy images export this directory.
func (m *Manager) WriteControlFiles(vm *state.VMRecord, vsock bool) error {
	controlDir := m.store.ControlShareDir(vm)
	if err := os.MkdirAll(controlDir, 0o755); err != nil {
		return err
	}
	guestPath := state.ControlShareGuestPath(vm.Arch)
	payload := map[string]any{
		"hostname": hostnameFor(vm.Name),
		"controlShare": map[string]any{
			"tag":        state.ControlShareTag,
			"guestPath":  guestPath,
			"egressPath": egress.GuestManifest,
			"portsPath":  filepath.Join(guestPath, "ports.json"),
			"mountsPath": filepath.Join(guestPath, "mounts.json"),
		},
	}
	if vsock {
		delete(payload, "controlShare")
		payload["control"] = map[string]any{"transport": "vsock", "version": 1, "guestPath": guestPath, "egressPath": filepath.Join(guestPath, "egress.json"), "mountsPath": filepath.Join(guestPath, "mounts.json")}
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
	rt, err := m.virtiofsRuntime(vm, share.Runtime{
		Tag:      state.ControlShareTag,
		HostPath: m.store.ControlShareDir(vm),
		LogKind:  "virtiofs-" + state.ControlShareTag,
	})
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
	return m.virtiofsRuntime(vm, share.Runtime{
		Tag:       sh.Tag,
		HostPath:  sh.HostPath,
		GuestPath: sh.GuestPath,
		Readonly:  sh.Readonly,
		LogKind:   "virtiofs-" + sh.Tag,
	})
}

func (m *Manager) virtiofsRuntime(vm *state.VMRecord, runtime share.Runtime) (share.Runtime, error) {
	paths := m.store.Runtime(vm).Virtiofsd(runtime.Tag)
	if err := share.ValidateUnixSocketPath("share "+runtime.Tag, paths.Socket); err != nil {
		return share.Runtime{}, err
	}
	runtime.Sock = paths.Socket
	runtime.ProcessRecord = paths.ProcessRecord
	return runtime, nil
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
	for _, paths := range rt.FindVirtiofsd() {
		service := runtimeService{
			label:     "virtiofsd",
			kind:      "virtiofsd",
			record:    paths.ProcessRecord,
			artifacts: []string{paths.Socket, paths.LockFile},
			timeout:   virtiofsdShutdownTimeout,
		}
		if _, err := stopService(service); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func requireShareMount(im *state.ImageRecord) error {
	if (!im.Capabilities.ControlShare && !im.Capabilities.VsockControl) || !im.Capabilities.GuestShareMount {
		return fmt.Errorf("share mounting unavailable for image %q: requires controlShare and guestShareMount (legacy) or vsockControl and guestShareMount capabilities; import an image with guest share-mount support", im.Name)
	}
	return nil
}

func validateImageFeatures(vm *state.VMRecord, im *state.ImageRecord) error {
	if len(vm.Shares) > 0 {
		if err := requireShareMount(im); err != nil {
			return err
		}
	}
	if vm.Network.AutoForward {
		return RequireGuestPortReport(im, "auto-forwarding")
	}
	return nil
}

func validateImageControl(vm *state.VMRecord, im *state.ImageRecord) error {
	if vm.MacOS != nil && im.Capabilities.ControlShare && !im.Capabilities.VsockControl {
		return fmt.Errorf("MacOS guest services require vsockControl; test the revised agent in a disposable clone or re-bake image %q", im.Name)
	}
	return nil
}
