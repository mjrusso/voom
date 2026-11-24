package state

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mjrusso/voom/internal/forward"
)

func TestPreviousRuntimeGuardIgnoresOnlyClosedVFKitClientSockets(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "voom-legacy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	vm := &VMRecord{ID: "vm", Name: "vm", Driver: "vfkit"}
	rt := RuntimeAt(root, vm)
	dir := rt.Dir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	gatewayAddr := &net.UnixAddr{Name: filepath.Join(root, "gateway.sock"), Net: "unixgram"}
	gateway, err := net.ListenUnixgram("unixgram", gatewayAddr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })
	path := filepath.Join(dir, "vfkit-104d3-d3f8.sock")
	conn, err := net.DialUnix("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"}, gatewayAddr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := CheckPreviousRuntimes(root); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("expected the live vfkit socket to block startup, got %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("closed socket did not leave a pathname: %v", err)
	}
	if err := CheckPreviousRuntimes(root); err != nil {
		t.Fatalf("closed vfkit socket blocked startup: %v", err)
	}
	if err := CheckPreviousVMRuntime(root, vm); err != nil {
		t.Fatalf("closed vfkit socket blocked VM operations: %v", err)
	}
	for _, path := range []string{rt.VFKitNetSock(), filepath.Join(dir, "vfkit-dead-helper.sock")} {
		socket, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
		if err != nil {
			t.Fatal(err)
		}
		if err := socket.Close(); err != nil {
			t.Fatal(err)
		}
		if err := CheckPreviousRuntimes(root); err == nil || !strings.Contains(err.Error(), path) {
			t.Errorf("expected %s to block startup, got %v", path, err)
		}
		if err := CheckPreviousVMRuntime(root, vm); err == nil || !strings.Contains(err.Error(), path) {
			t.Errorf("expected %s to block VM operations, got %v", path, err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPreviousRuntimeArtifactsFenceUpgrade(t *testing.T) {
	root := t.TempDir()
	vm := &VMRecord{ID: "owner", Name: "owner", Driver: "vfkit"}
	rt := RuntimeAt(root, vm)
	if err := os.MkdirAll(rt.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rt.Dir(), "efi-variable-store"), []byte("saved EFI variables"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{rt.RuntimeWatcherProcessRecord(), rt.VFKitRestSock(), rt.ForwardMutation(), rt.AutoForwardsJSON()} {
		if err := CheckPreviousRuntimes(root); err != nil {
			t.Fatalf("no legacy session artifacts: %v", err)
		}
		if err := os.WriteFile(path, []byte("legacy artifact"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := CheckPreviousRuntimes(root); err == nil {
			t.Errorf("global upgrade guard ignored %s", path)
		}
		if err := CheckPreviousVMRuntime(root, vm); err == nil {
			t.Errorf("VM upgrade guard ignored %s", path)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPreviousRuntimeGuardIgnoresSymlinkedDirectories(t *testing.T) {
	target := t.TempDir()
	vm := &VMRecord{ID: "vm", Name: "vm", Driver: "vfkit"}
	rt := RuntimeAt(target, vm)
	if err := os.MkdirAll(rt.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.GVProxyProcessRecord(), []byte("artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	rootLink := filepath.Join(t.TempDir(), "old-runtime")
	if err := os.Symlink(target, rootLink); err != nil {
		t.Fatal(err)
	}
	nestedRoot := t.TempDir()
	if err := os.Symlink(filepath.Join(target, "vms"), filepath.Join(nestedRoot, "vms")); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{rootLink, nestedRoot} {
		if err := CheckPreviousRuntimes(root); err != nil {
			t.Errorf("global guard followed symlink under %s: %v", root, err)
		}
		if err := CheckPreviousVMRuntime(root, vm); err != nil {
			t.Errorf("VM guard followed symlink under %s: %v", root, err)
		}
	}
}

func TestSSHPortReservationsIncludeRuntimeForwards(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, status string
		journal, reserved      bool
	}{
		{"active", "tcp", "active", false, true},
		{"journal", "tcp", "skipped", true, true},
		{"skipped", "tcp", "skipped", false, false},
		{"udp", "udp", "skipped", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := newTestStore(t, WithHostPortAvailable(func(string, int) (bool, string) { return true, "" }))
			vm := &VMRecord{SchemaVersion: SchemaVersion, ID: "owner", Name: "owner", Driver: "qemu"}
			if err := os.MkdirAll(st.VMDir(vm.ID), 0755); err != nil {
				t.Fatal(err)
			}
			if err := st.SaveVM(vm); err != nil {
				t.Fatal(err)
			}
			if err := st.RegisterVM(vm.Name, vm.ID); err != nil {
				t.Fatal(err)
			}
			rt := st.Runtime(vm)
			rows := []forward.RuntimeAuto{{Protocol: tc.protocol, Bind: "0.0.0.0", HostPort: SSHLow, GuestPort: 8080, Status: forward.ReservationState(tc.status)}}
			if err := forward.WriteRuntimeState(rt.AutoForwardsJSON(), rows); err != nil {
				t.Fatal(err)
			}
			if tc.journal {
				if err := WriteJSONAtomic(rt.ForwardMutation(), []forward.Decl{rows[0].Endpoint()}); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.EnsureSSHPortAvailable(SSHLow); (err != nil) != tc.reserved {
				t.Fatalf("SSH reservation: %v", err)
			}
			port, err := st.AllocateSSHPort()
			want := SSHLow
			if tc.reserved {
				want++
			}
			if err != nil || port != want {
				t.Fatalf("allocated %d, want %d: %v", port, want, err)
			}
			reserved, err := st.PortReservations(vm.ID)
			if err != nil || reserved.Reserved("tcp", "127.0.0.1", SSHLow) != (tc.journal && tc.protocol == "tcp") {
				t.Fatalf("own journal reservations: %v", err)
			}
			vm.Network.Forwards = []forward.Decl{{Protocol: "tcp", Bind: "127.0.0.1", HostPort: SSHLow, GuestPort: 8080}}
			if err := st.SaveVM(vm); err != nil {
				t.Fatal(err)
			}
			reserved, err = st.PortReservations(vm.ID)
			if err != nil || !reserved.Reserved("tcp", "127.0.0.1", SSHLow) {
				t.Fatalf("own declaration: %v", err)
			}
		})
	}
}

func TestMutationReservationsSurviveDeclarationChanges(t *testing.T) {
	st, _ := newTestStore(t)
	vm := &VMRecord{SchemaVersion: SchemaVersion, ID: "owner", Name: "owner", Driver: "qemu", Network: VMNetwork{SSHBind: "127.0.0.1", SSHPort: 2222}}
	if err := os.MkdirAll(st.VMDir(vm.ID), 0755); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVM(vm); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(vm.Name, vm.ID); err != nil {
		t.Fatal(err)
	}
	mutation := st.Runtime(vm).ForwardMutation()
	endpoints := []forward.Decl{
		{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 2222, GuestPort: 22},
		{Protocol: "udp", Bind: "0.0.0.0", HostPort: 5353, GuestPort: 5353},
	}
	if err := WriteJSONAtomic(mutation, endpoints); err != nil {
		t.Fatal(err)
	}
	vm.Network.SSHPort = 2223
	if err := st.SaveVM(vm); err != nil {
		t.Fatal(err)
	}
	snapshot, err := st.PortReservations(vm.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range endpoints {
		if !snapshot.Reserved(endpoint.Protocol, "127.0.0.1", endpoint.HostPort) {
			t.Fatalf("lost mutation endpoint: %#v", endpoint)
		}
	}
	if snapshot.Reserved("tcp", "127.0.0.1", 5353) || !snapshot.Reserved("tcp", "127.0.0.1", 2223) {
		t.Fatal("snapshot lost transport separation or current SSH declaration")
	}
	if err := os.Remove(mutation); err != nil {
		t.Fatal(err)
	}
	current, err := st.PortReservations("")
	if err != nil || current.Reserved("tcp", "127.0.0.1", 2222) || current.Reserved("udp", "127.0.0.1", 5353) {
		t.Fatalf("cleared mutation remains reserved: %v", err)
	}
	if !snapshot.Reserved("tcp", "127.0.0.1", 2222) {
		t.Fatal("previous snapshot changed after mutation cleanup")
	}
	for _, malformed := range []string{"{broken", "pending\n"} {
		if err := os.WriteFile(mutation, []byte(malformed), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := st.PortReserved("tcp", "127.0.0.1", 2222); err == nil {
			t.Fatalf("malformed mutation %q was treated as an available port", malformed)
		}
	}
}
