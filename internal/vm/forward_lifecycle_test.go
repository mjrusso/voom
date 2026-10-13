package vm

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
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
	m := New(st)
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
			var listener io.Closer
			var err error
			if row.Protocol == "udp" {
				listener, err = net.ListenPacket("udp", row.Local)
			} else {
				listener, err = net.Listen("tcp", row.Local)
			}
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			installed[row] = listener
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	_ = http.Serve(ln, handler)
}
