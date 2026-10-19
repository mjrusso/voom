package vm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mjrusso/voom/internal/control"
	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

type controlEndpoint struct {
	CID  uint32 `json:"cid"`
	Port uint32 `json:"port"`
}

func (m *Manager) startControl(ctx context.Context, vm *state.VMRecord, cid uint32) error {
	rt := m.store.Runtime(vm)
	if err := os.Remove(rt.ControlEndpoint()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := m.startSelfRecorded([]string{"debug", "control", vm.ID, strconv.FormatUint(uint64(cid), 10)}, m.store.LogPath(vm, "control"), rt.ControlProcessRecord()); err != nil {
		return err
	}
	err := waitFor(ctx, func() bool {
		_, running := process.ValidRecord(rt.ControlProcessRecord(), "control")
		_, readyErr := os.Stat(rt.ControlEndpoint())
		return !running || readyErr == nil
	}, driverStartTimeout)
	if _, running := process.ValidRecord(rt.ControlProcessRecord(), "control"); !running || err != nil {
		return fmt.Errorf("vsock control service failed to start: %v; see %s", err, m.store.LogPath(vm, "control"))
	}
	return nil
}

func (m *Manager) stopControl(vm *state.VMRecord) error {
	rt := m.store.Runtime(vm)
	_, err := stopService(runtimeService{label: "control", kind: "control", record: rt.ControlProcessRecord(), artifacts: []string{rt.ControlSock(), rt.ControlEndpoint(), m.store.ControlSharePortsPath(vm)}, timeout: processStopTimeout})
	return err
}

// WatchControl serves one VM until its driver exits or fails to start within a minute.
func (m *Manager) WatchControl(ctx context.Context, id string, cid uint32) error {
	vm, err := m.store.LoadVMByID(id)
	if err != nil {
		return err
	}
	rt := m.store.Runtime(vm)
	listener, port, err := control.ListenHost(vm.Driver, rt.ControlSock(), cid)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	if err := state.WriteJSONAtomic(rt.ControlEndpoint(), controlEndpoint{cid, port}); err != nil {
		return err
	}
	deadline := time.Now().Add(time.Minute)
	seenDriver := false
	for ctx.Err() == nil {
		if m.IsRunning(vm) {
			seenDriver = true
		} else if seenDriver || time.Now().After(deadline) {
			return nil
		}
		stream, err := listener.Accept()
		if err != nil {
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				fmt.Fprintln(os.Stderr, err)
			}
			continue
		}
		err = m.controlSession(vm, stream)
		_ = stream.Close()
		_ = os.Remove(m.store.ControlSharePortsPath(vm))
		if err != nil {
			fmt.Fprintln(os.Stderr, "control session:", err)
		}
	}
	return ctx.Err()
}

func (m *Manager) controlSession(vm *state.VMRecord, stream control.Stream) error {
	defer func() { _ = os.Remove(m.store.ControlSharePortsPath(vm)) }()
	if err := stream.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(stream, control.MaxMessage)
	if _, err := control.Read(reader, "hello"); err != nil {
		return err
	}
	for {
		if err := stream.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
			return err
		}
		config, err := m.controlConfig(vm)
		if err != nil {
			return err
		}
		if err := control.Write(stream, "config", config); err != nil {
			return err
		}
		raw, err := control.Read(reader, "report")
		if err != nil {
			return err
		}
		var report struct {
			Ports  forward.PortsReport `json:"ports"`
			Errors []string            `json:"errors"`
		}
		if err := json.Unmarshal(raw, &report); err != nil {
			return err
		}
		if report.Ports.GeneratedAt.IsZero() {
			return errors.New("guest report is missing generatedAt")
		}
		report.Ports.GeneratedAt = time.Now().UTC()
		if err := forward.ValidateReport(report.Ports); err != nil {
			return err
		}
		if err := state.WriteJSONAtomic(m.store.ControlSharePortsPath(vm), report.Ports); err != nil {
			return err
		}
		if err := state.WriteJSONAtomic(filepath.Join(m.store.ControlShareDir(vm), "status.json"), report.Errors); err != nil {
			return err
		}
		for _, message := range report.Errors {
			fmt.Fprintln(os.Stderr, message)
		}
	}
}

func (m *Manager) controlConfig(vm *state.VMRecord) (map[string]any, error) {
	dir := m.store.ControlShareDir(vm)
	var metadata, mounts json.RawMessage
	if err := state.ReadJSON(filepath.Join(dir, "metadata.json"), &metadata); err != nil {
		return nil, err
	}
	if err := state.ReadJSON(filepath.Join(dir, "mounts.json"), &mounts); err != nil {
		return nil, err
	}
	config := map[string]any{"metadata": metadata, "mounts": mounts}
	var manifest json.RawMessage
	rt := m.store.Runtime(vm)
	if err := state.ReadJSON(rt.EgressManifest(), &manifest); err == nil {
		ca, err := os.ReadFile(rt.EgressCA())
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if vm.MacOS != nil {
			var guestManifest map[string]any
			if err := json.Unmarshal(manifest, &guestManifest); err != nil {
				return nil, err
			}
			if len(ca) > 0 {
				guestManifest["caCertificate"] = filepath.Join(state.ControlShareGuestPath(vm.Arch), "egress-ca.pem")
			}
			adjusted, err := json.Marshal(guestManifest)
			if err != nil {
				return nil, err
			}
			manifest = adjusted
		}
		config["egress"], config["egressCA"] = manifest, string(ca)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return config, nil
}
