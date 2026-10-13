package vm

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/driver/qemu"
	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/host"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

func TestManualForwardsSurviveManagerRestart(t *testing.T) {
	st, m, rec := newForwardLifecycleVM(t)
	ctx := context.Background()
	t.Cleanup(func() {
		if _, err := m.Stop(ctx, rec.Name); err != nil {
			t.Error(err)
		}
	})
	port := freeLoopbackPort(t)
	for port == rec.Network.SSHPort || portBusy("udp", "127.0.0.1", port) {
		port = freeLoopbackPort(t)
	}
	for _, protocol := range []string{"tcp", "udp"} {
		if _, err := m.AddForward(ctx, rec.Name, 5353, port, "127.0.0.1", false, protocol); err != nil {
			t.Fatal(err)
		}
	}
	for cycle := 0; cycle < 2; cycle++ {
		st, err := state.Open()
		if err != nil {
			t.Fatal(err)
		}
		m = New(st)
		persisted, err := st.LoadVM(rec.Name)
		if err != nil {
			t.Fatal(err)
		}
		if len(persisted.Network.Forwards) != 2 {
			t.Fatal("declarations did not persist")
		}
		if _, err := m.Start(ctx, io.Discard, rec.Name); err != nil {
			t.Fatal(err)
		}
		installed, err := gvproxy.Forwards(ctx, st.Runtime(persisted).NetworkSock())
		if err != nil {
			t.Fatal(err)
		}
		expected := gvproxy.Forward{Protocol: "udp", Local: forward.Address("127.0.0.1", port), Remote: forward.Address(m.GuestTargetIP(persisted), 5353)}
		if _, ok := installed[expected]; !ok {
			t.Fatalf("cycle %d: UDP forward absent: %#v", cycle, installed)
		}
		for _, protocol := range []string{"tcp", "udp"} {
			if available, _ := forward.ProtocolPortAvailable(protocol, "127.0.0.1", port); available {
				t.Fatalf("cycle %d: %s listener is absent", cycle, protocol)
			}
		}
		if changed, err := m.Stop(ctx, rec.Name); err != nil || !changed {
			t.Fatalf("cycle %d stop: changed=%t err=%v", cycle, changed, err)
		}
		// Process exit does not guarantee an immediate successful rebind.
		for _, protocol := range []string{"tcp", "udp"} {
			if err := waitFor(ctx, func() bool {
				available, _ := forward.ProtocolPortAvailable(protocol, "127.0.0.1", port)
				return available
			}, time.Second); err != nil {
				t.Fatalf("cycle %d: %s socket was not released after stop: %v", cycle, protocol, err)
			}
		}
	}
	t.Setenv("VOOM_FORWARD_TEST_NO_UDP", "1")
	if _, err := m.Start(ctx, io.Discard, rec.Name); err == nil || !strings.Contains(err.Error(), "udp-forward-v1") {
		t.Fatalf("unsupported UDP startup: %v", err)
	}
	if process.HasRecord(st.Runtime(rec).GVProxyProcessRecord()) {
		t.Fatal("unsupported startup left a backend process record")
	}
}

func newForwardLifecycleVM(t *testing.T) (*state.Store, *Manager, *state.VMRecord) {
	t.Helper()
	driver := state.DefaultDriver()
	if err := host.ValidateHostImage(host.System(), state.DiskFormat(driver), driver); err != nil {
		t.Skip(err)
	}
	st, dir := newTestStore(t)
	im := &state.ImageRecord{
		SchemaVersion: state.SchemaVersion,
		ID:            "forward-image", Name: "forward-image",
		Arch: host.System(), Format: state.DiskFormat(driver),
		Metadata: state.ImageMetadata{KernelPath: "fixture-kernel"},
	}
	if err := os.MkdirAll(st.ImageDir(im.ID), 0755); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(im.ID), "image.json"), im); err != nil {
		t.Fatal(err)
	}
	rec := &state.VMRecord{
		SchemaVersion: state.SchemaVersion,
		ID:            "restart", Name: "restart", Driver: driver, Arch: im.Arch,
		Image:     state.VMImageRef{ID: im.ID},
		Access:    state.VMAccess{SSHUser: "test", SSHIdentityPath: filepath.Join(dir, "fixture-key")},
		Resources: state.VMResources{CPUs: 1, MemoryMiB: 256},
		Network:   state.VMNetwork{SSHBind: "127.0.0.1", SSHPort: freeLoopbackPort(t)},
	}
	if err := os.WriteFile(rec.Access.SSHIdentityPath+".pub", []byte("ssh-ed25519 AAAATEST fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(st.VMDir(rec.ID), 0755); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVM(rec); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(rec.Name, rec.ID); err != nil {
		t.Fatal(err)
	}
	installForwardLifecycleExecutables(t, dir, rec.Arch)
	return st, New(st), rec
}

func installForwardLifecycleExecutables(t *testing.T, dir, arch string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VOOM_FORWARD_TEST_PROCESS", "1")
	t.Setenv("VOOM_FORWARD_TEST_HELPER", exe)
	for name, role := range map[string]string{"gvproxy": "gvproxy", qemu.ExecutableName(arch): "qemu", "vfkit": "vfkit", "qemu-img": "qemu-img"} {
		path := filepath.Join(dir, name)
		script := "#!/usr/bin/env bash\nexec -a " + name + " \"$VOOM_FORWARD_TEST_HELPER\" -test.run '^TestForwardLifecycleProcess$' -- " + role + " \"$@\"\n"
		if err := os.WriteFile(path, []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
		t.Setenv(host.ExeOverrideName(name), path)
	}
	if strings.HasPrefix(arch, "aarch64") {
		firmware := filepath.Join(dir, "firmware")
		if err := os.WriteFile(firmware, nil, 0644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("VOOM_QEMU_AARCH64_UEFI", firmware)
	}
}

func TestForwardLifecycleProcess(t *testing.T) {
	if os.Getenv("VOOM_FORWARD_TEST_PROCESS") != "1" {
		return
	}
	index := slices.Index(os.Args, "--")
	args := os.Args[index+1:]
	role := args[0]
	args = args[1:]
	option := func(flag string) string {
		i := slices.Index(args, flag)
		if i < 0 || i+1 >= len(args) {
			t.Fatalf("missing %s in %v", flag, args)
		}
		return args[i+1]
	}
	if role == "forward" {
		st, err := state.Open()
		if err != nil {
			t.Fatal(err)
		}
		port, err := strconv.Atoi(args[2])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := New(st).AddForward(context.Background(), args[0], 5353, port, "127.0.0.1", false, args[1]); err != nil {
			t.Fatal(err)
		}
		return
	}
	if role == "qemu-img" {
		return
	}
	if role == "qemu" {
		addr := strings.TrimSuffix(strings.TrimPrefix(option("-qmp"), "unix:"), ",server=on,wait=off")
		ln, err := net.Listen("unix", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = ln.Close() }()
		for {
			conn, err := ln.Accept()
			if err != nil {
				t.Fatal(err)
			}
			_ = conn.Close()
		}
	}
	if role == "vfkit" {
		ln, err := net.Listen("unix", strings.TrimPrefix(option("--restful-uri"), "unix://"))
		if err != nil {
			t.Fatal(err)
		}
		_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			os.Exit(0)
		}))
		return
	}
	caps := map[string]int{gvproxy.GuestIsolationCapability: 1, gvproxy.UDPForwardCapability: 1}
	if os.Getenv("VOOM_FORWARD_TEST_NO_UDP") == "1" {
		delete(caps, gvproxy.UDPForwardCapability)
	}
	if slices.Contains(args, "-capabilities") {
		_ = json.NewEncoder(os.Stdout).Encode(caps)
		os.Exit(0)
	}
	var network io.Closer
	var err error
	if slices.Contains(args, "-listen-qemu") {
		network, err = net.Listen("unix", strings.TrimPrefix(option("-listen-qemu"), "unix://"))
	} else {
		network, err = net.ListenPacket("unixgram", strings.TrimPrefix(option("-listen-vfkit"), "unixgram://"))
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = network.Close() }()
	ln, err := net.Listen("unix", strings.TrimPrefix(option("-listen"), "unix://"))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	installed := map[gvproxy.Forward]io.Closer{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/services/gateway-forward/capabilities":
			_ = json.NewEncoder(w).Encode(caps)
		case "/services/forwarder/all":
			rows := make([]gvproxy.Forward, 0, len(installed))
			for row := range installed {
				rows = append(rows, row)
			}
			_ = json.NewEncoder(w).Encode(rows)
		case "/services/forwarder/expose":
			var row gvproxy.Forward
			if err := json.NewDecoder(r.Body).Decode(&row); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			failure := os.Getenv("VOOM_FORWARD_TEST_EXPOSE_FAILURE")
			if failure == "reject" && !strings.HasSuffix(row.Remote, ":22") {
				http.Error(w, "invalid forward", http.StatusBadRequest)
				return
			}
			if failure == "conflict" && !strings.HasSuffix(row.Remote, ":22") {
				http.Error(w, "host port is already in use", http.StatusConflict)
				return
			}
			var listener io.Closer
			var err error
			if row.Protocol == "udp" {
				listener, err = net.ListenPacket("udp", row.Local)
			} else {
				listener, err = net.Listen("tcp", row.Local)
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			installed[row] = listener
			if !strings.HasSuffix(row.Remote, ":22") {
				switch failure {
				case "lost":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = conn.Close()
					}
				case "timeout":
					<-r.Context().Done()
				}
			}
		case "/services/forwarder/unexpose":
			var row gvproxy.Forward
			if err := json.NewDecoder(r.Body).Decode(&row); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			for key, listener := range installed {
				if key.Protocol == row.Protocol && key.Local == row.Local {
					_ = listener.Close()
					delete(installed, key)
				}
			}
			if os.Getenv("VOOM_FORWARD_TEST_UNEXPOSE_FAILURE") == "lost" {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
				}
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	_ = http.Serve(ln, handler)
}

func TestManualForwardRecoversUncertainMutation(t *testing.T) {
	for _, tc := range []struct{ protocol, failure string }{{"tcp", "lost"}, {"udp", "lost"}, {"udp", "timeout"}, {"tcp", "reject"}, {"tcp", "conflict"}, {"udp", "conflict"}} {
		t.Run(tc.protocol+"-"+tc.failure, func(t *testing.T) {
			t.Setenv("VOOM_FORWARD_TEST_EXPOSE_FAILURE", tc.failure)
			st, _, rec := newForwardLifecycleVM(t)
			recorder := &recordingEmitter{}
			m := New(st, WithEventEmitter(recorder))
			ctx := context.Background()
			t.Cleanup(func() { _, _ = m.Stop(ctx, rec.Name) })
			if _, err := m.Start(ctx, io.Discard, rec.Name); err != nil {
				t.Fatal(err)
			}
			recorder.events = nil
			port := freeLoopbackPort(t)
			_, err := m.AddForward(ctx, rec.Name, 5353, port, "127.0.0.1", false, tc.protocol)
			if err == nil {
				t.Fatal("failed expose reported success")
			}
			if tc.failure == "reject" || tc.failure == "conflict" {
				if !m.IsRunning(rec) {
					t.Fatal("pre-mutation rejection stopped VM")
				}
				assertVMStopEvents(t, recorder, rec.ID, 0)
			} else {
				if m.IsRunning(rec) || process.HasRecord(st.Runtime(rec).GVProxyProcessRecord()) {
					t.Fatalf("uncertain mutation left runtime alive: %v", err)
				}
				if !strings.Contains(err.Error(), "VM stopped") {
					t.Fatalf("recovery error: %v", err)
				}
				assertVMStopEvents(t, recorder, rec.ID, 1)
			}
			persisted, err := st.LoadVM(rec.Name)
			if err != nil || len(persisted.Network.Forwards) != 0 {
				t.Fatalf("confirmed rejection/termination left declaration: %#v %v", persisted, err)
			}
			if err := waitFor(ctx, func() bool {
				available, _ := forward.ProtocolPortAvailable(tc.protocol, "127.0.0.1", port)
				return available
			}, time.Second); err != nil {
				t.Fatal("recovery left host listener bound", err)
			}
		})
	}
}

func TestManualForwardUncertainRemovalKeepsDeclaration(t *testing.T) {
	st, _, rec := newForwardLifecycleVM(t)
	recorder := &recordingEmitter{}
	m := New(st, WithEventEmitter(recorder))
	ctx := context.Background()
	t.Cleanup(func() { _, _ = m.Stop(ctx, rec.Name) })
	port := freeLoopbackPort(t)
	if _, err := m.AddForward(ctx, rec.Name, 5353, port, "127.0.0.1", false, "udp"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VOOM_FORWARD_TEST_UNEXPOSE_FAILURE", "lost")
	if _, err := m.Start(ctx, io.Discard, rec.Name); err != nil {
		t.Fatal(err)
	}
	recorder.events = nil
	if err := m.RemoveForward(ctx, rec.Name, port, "127.0.0.1", "udp"); err == nil || !strings.Contains(err.Error(), "could not remove") {
		t.Fatalf("uncertain removal: %v", err)
	}
	if m.IsRunning(rec) {
		t.Fatal("uncertain removal left runtime alive")
	}
	assertVMStopEvents(t, recorder, rec.ID, 1)
	persisted, err := st.LoadVM(rec.Name)
	if err != nil || len(persisted.Network.Forwards) != 1 {
		t.Fatalf("lost removal declaration: %#v %v", persisted, err)
	}
	if err := m.RemoveForward(ctx, rec.Name, port, "127.0.0.1", "udp"); err != nil {
		t.Fatal(err)
	}
}

func TestUnconfirmedManualForwardRetainsReservationAcrossCommands(t *testing.T) {
	for _, protocol := range []string{"tcp", "udp"} {
		t.Run(protocol, func(t *testing.T) {
			t.Setenv("VOOM_FORWARD_TEST_EXPOSE_FAILURE", "lost")
			st, m, rec := newForwardLifecycleVM(t)
			ctx := context.Background()
			if _, err := m.Start(ctx, io.Discard, rec.Name); err != nil {
				t.Fatal(err)
			}
			record := st.Runtime(rec).GVProxyProcessRecord()
			identity, err := os.ReadFile(record)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = os.WriteFile(record, identity, 0600)
				_, _ = m.Stop(ctx, rec.Name)
			})
			if err := os.Remove(record); err != nil {
				t.Fatal(err)
			}
			port := freeLoopbackPort(t)
			if _, err := m.AddForward(ctx, rec.Name, 5353, port, "127.0.0.1", false, protocol); err == nil || !strings.Contains(err.Error(), "reservation retained") {
				t.Fatalf("unknown installation: %v", err)
			}
			m = New(st)
			persisted, err := st.LoadVM(rec.Name)
			if err != nil || len(persisted.Network.Forwards) != 1 {
				t.Fatalf("lost declaration: %#v %v", persisted, err)
			}
			if available, _ := forward.ProtocolPortAvailable(protocol, "127.0.0.1", port); available {
				t.Fatal("lost response did not leave live listener")
			}
			reserved, err := st.PortReserved(protocol, "0.0.0.0", port)
			if err != nil || !reserved {
				t.Fatalf("unconfirmed wildcard reservation: %t %v", reserved, err)
			}
			rows, err := m.ForwardRows(ctx, []string{rec.Name})
			if err != nil || len(rows) != 2 || rows[1].Installed || rows[1].Status != "uncertain" {
				t.Fatalf("unconfirmed status: %#v %v", rows, err)
			}
			if err := m.RemoveForward(ctx, rec.Name, port, "127.0.0.1", protocol); err == nil {
				t.Fatal("retry released uncertain reservation")
			}
			// A stopped driver does not prove that gvproxy has stopped.
			if _, err := m.Stop(ctx, rec.Name); err == nil {
				t.Fatal("unrecorded backend stop reported success")
			}
			if err := m.RemoveForward(ctx, rec.Name, port, "127.0.0.1", protocol); err == nil {
				t.Fatal("stopped-driver removal released uncertain reservation")
			}
			persisted, err = st.LoadVM(rec.Name)
			if err != nil || len(persisted.Network.Forwards) != 1 {
				t.Fatalf("retry lost declaration: %#v %v", persisted, err)
			}
			if err := os.WriteFile(record, identity, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Stop(ctx, rec.Name); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(st.Runtime(rec).ForwardMutation()); !os.IsNotExist(err) {
				t.Fatalf("confirmed shutdown retained marker: %v", err)
			}
			if err := m.RemoveForward(ctx, rec.Name, port, "127.0.0.1", protocol); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInterruptedManualForwardRetainsReservation(t *testing.T) {
	t.Setenv("VOOM_FORWARD_TEST_EXPOSE_FAILURE", "timeout")
	st, m, rec := newForwardLifecycleVM(t)
	ctx := context.Background()
	t.Cleanup(func() { _, _ = m.Stop(ctx, rec.Name) })
	if _, err := m.Start(ctx, io.Discard, rec.Name); err != nil {
		t.Fatal(err)
	}
	port := freeLoopbackPort(t)
	cmd := exec.Command(os.Getenv("VOOM_FORWARD_TEST_HELPER"), "-test.run", "^TestForwardLifecycleProcess$", "--", "forward", rec.Name, "udp", strconv.Itoa(port))
	outputPath := filepath.Join(t.TempDir(), "forward-child.log")
	outputFile, err := os.Create(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = outputFile.Close() }()
	cmd.Stdout, cmd.Stderr = outputFile, outputFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	childDone := make(chan struct{})
	var childErr error
	go func() {
		childErr = cmd.Wait()
		close(childDone)
	}()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-childDone })
	failChild := func(reason string) {
		t.Helper()
		_ = cmd.Process.Kill()
		<-childDone
		output, readErr := os.ReadFile(outputPath)
		t.Fatalf("%s: child exit=%v, output read=%v\n%s", reason, childErr, readErr, output)
	}
	// Keep readiness below gvproxy's five-second request timeout.
	if err := waitFor(ctx, func() bool {
		select {
		case <-childDone:
			return true
		default:
			return portBusy("udp", "127.0.0.1", port)
		}
	}, 4*time.Second); err != nil {
		failChild("backend did not install: " + err.Error())
	}
	select {
	case <-childDone:
		failChild("forwarding child exited before interruption")
	default:
	}
	if err := cmd.Process.Kill(); err != nil {
		failChild("could not interrupt forwarding child: " + err.Error())
	}
	<-childDone
	if exitErr, interrupted := childErr.(*exec.ExitError); !interrupted || exitErr.ExitCode() != -1 {
		failChild("forwarding child completed instead of being interrupted")
	}
	if _, err := os.Stat(st.Runtime(rec).ForwardMutation()); err != nil {
		failChild("interrupted command lost mutation marker: " + err.Error())
	}
	persisted, err := st.LoadVM(rec.Name)
	if err != nil || len(persisted.Network.Forwards) != 1 {
		t.Fatalf("interrupted command lost declaration: %#v %v", persisted, err)
	}
	m = New(st)
	if err := m.RemoveForward(ctx, rec.Name, port, "127.0.0.1", "udp"); err == nil || !strings.Contains(err.Error(), "VM stopped") {
		t.Fatalf("interrupted command recovery: %v", err)
	}
	if m.IsRunning(rec) {
		t.Fatal("recovery left VM running")
	}
	if err := m.RemoveForward(ctx, rec.Name, port, "127.0.0.1", "udp"); err != nil {
		t.Fatal(err)
	}
}
