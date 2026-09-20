package vm

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/egress"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
	"github.com/mjrusso/voom/internal/usb"
)

func lifecycleTestStore(t *testing.T) *state.Store {
	t.Helper()
	st, _ := newTestStore(t)
	for _, name := range []string{"a", "b"} {
		record := &state.VMRecord{SchemaVersion: 1, ID: name, Name: name, Driver: "qemu", Image: state.VMImageRef{ID: "image1"}, UpdatedAt: time.Now().UTC()}
		if err := os.MkdirAll(st.VMDir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveVM(record); err != nil {
			t.Fatal(err)
		}
		if err := st.RegisterVM(name, name); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func TestCleanupAttemptsGatewayAfterOtherHelperFailure(t *testing.T) {
	st, _ := newTestStore(t)
	record := &state.VMRecord{ID: "cleanup", Name: "cleanup", Driver: "qemu"}
	rt := st.Runtime(record)
	if err := os.MkdirAll(rt.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := fakeNamedVMProcess(t, "gvproxy")
	writeProcessRecord(t, rt.GVProxyProcessRecord(), cmd)
	if err := os.WriteFile(rt.AutoForwardProcessRecord(), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	stop, err := New(st).stopRuntime(context.Background(), record)
	if err == nil || !stop.changed {
		t.Fatal("unknown helper identity was accepted")
	}
	if !stop.gatewayTerminationConfirmed {
		t.Fatal("unrelated helper failure obscured confirmed gateway termination")
	}
	if _, err := os.Stat(rt.GVProxyProcessRecord()); !os.IsNotExist(err) {
		t.Fatal("gateway termination was not attempted")
	}
	if _, err := os.Stat(rt.AutoForwardProcessRecord()); err != nil {
		t.Fatal("unknown helper record was discarded")
	}
}

func TestCleanupFindsVirtiofsProcessRecord(t *testing.T) {
	st, _ := newTestStore(t)
	record := &state.VMRecord{ID: "cleanup", Name: "cleanup", Driver: "qemu"}
	rt := st.Runtime(record)
	if err := os.MkdirAll(rt.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := fakeNamedVMProcess(t, "virtiofsd")
	paths := rt.Virtiofsd("test")
	if err := process.Record(paths.ProcessRecord, cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	if err := os.WriteFile(paths.LockFile, []byte("123\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stop, err := New(st).stopRuntime(context.Background(), record)
	if err != nil || !stop.changed {
		t.Fatalf("stopRuntime result=%+v error=%v", stop, err)
	}
	_, _ = cmd.Process.Wait()
	if process.Alive(cmd.Process.Pid) {
		t.Fatal("virtiofsd process remains")
	}
	if process.HasRecord(paths.ProcessRecord) {
		t.Fatal("virtiofsd process record remains")
	}
	for _, path := range []string{paths.Socket, paths.LockFile} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("virtiofsd artifact remains at %s: %v", path, err)
		}
	}
}

func TestCleanupRetainsVirtiofsArtifactsWithoutProcessRecord(t *testing.T) {
	st, _ := newTestStore(t)
	record := &state.VMRecord{ID: "cleanup", Name: "cleanup", Driver: "qemu"}
	rt := st.Runtime(record)
	if err := os.MkdirAll(rt.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	paths := rt.Virtiofsd("test")
	listener, err := net.Listen("unix", paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	if err := os.WriteFile(paths.LockFile, []byte("123\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stop, err := New(st).stopRuntime(context.Background(), record)
	if err == nil || !strings.Contains(err.Error(), "virtiofsd termination unconfirmed") || !stop.changed {
		t.Fatalf("stopRuntime result=%+v error=%v", stop, err)
	}
	for _, path := range []string{paths.Socket, paths.LockFile} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("virtiofsd artifact was removed from %s: %v", path, err)
		}
	}
}

func TestCleanupStopsVirtiofsServicesIndependently(t *testing.T) {
	st, _ := newTestStore(t)
	record := &state.VMRecord{ID: "cleanup", Name: "cleanup", Driver: "qemu"}
	rt := st.Runtime(record)
	if err := os.MkdirAll(rt.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	confirmed := rt.Virtiofsd("confirmed")
	cmd := fakeNamedVMProcess(t, "virtiofsd")
	if err := process.Record(confirmed.ProcessRecord, cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	confirmedListener, err := net.Listen("unix", confirmed.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = confirmedListener.Close() }()
	unconfirmed := rt.Virtiofsd("unconfirmed")
	unconfirmedListener, err := net.Listen("unix", unconfirmed.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unconfirmedListener.Close() }()
	stop, err := New(st).stopRuntime(context.Background(), record)
	if err == nil || !strings.Contains(err.Error(), "virtiofsd termination unconfirmed") || !stop.changed {
		t.Fatalf("stopRuntime result=%+v error=%v", stop, err)
	}
	_, _ = cmd.Process.Wait()
	for _, path := range []string{confirmed.ProcessRecord, confirmed.Socket} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("confirmed virtiofsd artifact remains at %s: %v", path, err)
		}
	}
	if _, err := os.Lstat(unconfirmed.Socket); err != nil {
		t.Fatalf("unconfirmed virtiofsd socket was removed: %v", err)
	}
}

func TestWaitingForVMLockDoesNotHoldGlobal(t *testing.T) {
	for _, action := range []string{"remove", "rename"} {
		t.Run(action, func(t *testing.T) {
			st := lifecycleTestStore(t)
			local, err := st.TryLockVM("a")
			if err != nil {
				t.Fatal(err)
			}
			if local == nil {
				t.Fatal("VM lock was unavailable")
			}
			defer local()
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			waiting := make(chan error, 1)
			go func() {
				if action == "remove" {
					waiting <- New(st).Remove(ctx, "a")
					return
				}
				_, err := st.RenameVM(ctx, "a", "renamed")
				waiting <- err
			}()
			// Let the operation enter its VM-lock wait before checking the global lock.
			time.Sleep(50 * time.Millisecond)
			other := make(chan error, 1)
			go func() { other <- New(st).Remove(context.Background(), "b") }()
			select {
			case err := <-other:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("unrelated VM blocked by per-VM lock")
			}
			select {
			case err := <-waiting:
				if err == nil {
					t.Fatal("ignored cancellation while waiting for VM")
				}
			case <-time.After(time.Second):
				t.Fatal("VM lock wait ignored cancellation")
			}
		})
	}
}

func TestCleanupReleasesGlobal(t *testing.T) {
	for _, action := range []string{"remove", "reset"} {
		t.Run(action, func(t *testing.T) {
			st := lifecycleTestStore(t)
			record, err := st.LoadVM("a")
			if err != nil {
				t.Fatal(err)
			}
			rt := st.Runtime(record)
			if err := os.MkdirAll(rt.Dir(), 0700); err != nil {
				t.Fatal(err)
			}
			driver := fakeNamedVMProcess(t, "qemu-system-test")
			writeProcessRecord(t, rt.VMProcessRecord(), driver)
			monitor, err := net.Listen("unix", rt.QEMUMonitor())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = monitor.Close() }()
			entered := make(chan struct{})
			go func() {
				conn, err := monitor.Accept()
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				encoder := json.NewEncoder(conn)
				decoder := json.NewDecoder(conn)
				_ = encoder.Encode(map[string]any{"QMP": map[string]any{"version": map[string]any{}, "capabilities": []string{}}})
				for {
					var request vmQMPRequest
					if err := decoder.Decode(&request); err != nil {
						return
					}
					if request.Execute == "qmp_capabilities" {
						_ = encoder.Encode(map[string]any{"return": map[string]any{}, "id": request.ID})
						continue
					}
					if request.Execute == "system_powerdown" {
						close(entered)
						return
					}
				}
			}()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			manager := New(st)
			go func() {
				switch action {
				case "remove":
					done <- manager.Remove(ctx, "a")
				case "reset":
					done <- manager.ResetDisk(ctx, "a", "missing-image")
				}
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("cleanup did not reach graceful shutdown")
			}
			other := make(chan error, 1)
			go func() { err := manager.Remove(context.Background(), "b"); other <- err }()
			select {
			case err := <-other:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("cleanup blocked unrelated configuration")
			}
			cancel()
			select {
			case err := <-done:
				if action != "remove" && err == nil {
					t.Fatal("expected operation to fail after cancellation")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cleanup did not finish")
			}
		})
	}
}

func TestSetEgressCleanupDoesNotHoldGlobal(t *testing.T) {
	st := lifecycleTestStore(t)
	vm, err := st.LoadVM("a")
	if err != nil {
		t.Fatal(err)
	}
	rt := st.Runtime(vm)
	if err := os.MkdirAll(rt.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	signaled := filepath.Join(t.TempDir(), "term-received")
	backendSocket := filepath.Join(t.TempDir(), "backend.sock")
	gvproxy := fakeNamedVMProcessIgnoringTERM(t, "gvproxy", signaled)
	writeProcessRecord(t, rt.GVProxyProcessRecord(), gvproxy)

	done := make(chan error, 1)
	go func() {
		decl := egress.Decl{Mode: egress.ModeExplicit, Enabled: true, BackendSocket: backendSocket}
		_, err := New(st).SetEgress(context.Background(), "a", decl, EgressOptions{})
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(signaled); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("egress cleanup did not signal gvproxy")
		}
		time.Sleep(10 * time.Millisecond)
	}

	acquired := make(chan error, 1)
	go func() { acquired <- st.WithGlobal(func() error { return nil }) }()
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("egress cleanup blocked the global lock")
	}
	if err := gvproxy.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("SetEgress succeeded with a missing backend socket")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SetEgress did not finish")
	}
}

func TestConcurrentPortReservationsCommitOnce(t *testing.T) {
	for _, action := range []string{"ssh", "forward"} {
		t.Run(action, func(t *testing.T) {
			st := lifecycleTestStore(t)
			manager := New(st)
			for i, name := range []string{"a", "b"} {
				vm, err := st.LoadVM(name)
				if err != nil {
					t.Fatal(err)
				}
				vm.Network.SSHBind = "127.0.0.1"
				vm.Network.SSHPort = 22000 + i
				if err := st.SaveVM(vm); err != nil {
					t.Fatal(err)
				}
			}
			port := freeLoopbackPort(t)
			results := make(chan error, 2)
			for _, name := range []string{"a", "b"} {
				go func() {
					if action == "ssh" {
						_, _, err := manager.SetSSHPort(context.Background(), name, port)
						results <- err
						return
					}
					_, err := manager.AddForward(context.Background(), name, 8080, port, "127.0.0.1", false)
					results <- err
				}()
			}
			successes := 0
			for range 2 {
				if err := <-results; err == nil {
					successes++
				}
			}
			if successes != 1 {
				t.Fatalf("successful reservations = %d, want 1", successes)
			}
		})
	}
}

func TestConcurrentUSBStartReservationsCommitOnce(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("USB passthrough is Linux-only")
	}
	st := lifecycleTestStore(t)
	manager := New(st)
	vms := make([]*state.VMRecord, 0, 2)
	pids := make([]int, 0, 2)
	for _, name := range []string{"a", "b"} {
		vmRec, err := st.LoadVM(name)
		if err != nil {
			t.Fatal(err)
		}
		vmRec.USBDevices = []usb.Decl{{Name: "board", Route: usb.Route{Controller: "0000:00:14.0", Protocol: 2, Port: "2"}}}
		if err := st.SaveVM(vmRec); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(st.Runtime(vmRec).Dir(), 0o755); err != nil {
			t.Fatal(err)
		}
		vms = append(vms, vmRec)
		pids = append(pids, fakeNamedVMProcess(t, "qemu-system-test").Process.Pid)
	}

	results := make(chan error, 2)
	for i, vmRec := range vms {
		go func() {
			results <- manager.withUSBStartReservation(context.Background(), vmRec, func() error {
				return process.Record(st.Runtime(vmRec).VMProcessRecord(), pids[i])
			})
		}()
	}
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		} else if !strings.Contains(err.Error(), "active or unconfirmed runtime") {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful USB start reservations = %d, want 1", successes)
	}
}

func TestUSBStartReservationReleasesGlobalLockBeforeStart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("USB passthrough is Linux-only")
	}
	st := lifecycleTestStore(t)
	vmRec, err := st.LoadVM("a")
	if err != nil {
		t.Fatal(err)
	}
	vmRec.USBDevices = []usb.Decl{{Name: "board", Route: usb.Route{Controller: "0000:00:14.0", Protocol: 2, Port: "2"}}}
	if err := st.SaveVM(vmRec); err != nil {
		t.Fatal(err)
	}

	startEntered := make(chan struct{})
	releaseStart := make(chan struct{})
	reservationResult := make(chan error, 1)
	go func() {
		reservationResult <- New(st).withUSBStartReservation(context.Background(), vmRec, func() error {
			close(startEntered)
			<-releaseStart
			return nil
		})
	}()
	<-startEntered

	globalResult := make(chan error, 1)
	go func() {
		globalResult <- st.WithGlobal(func() error { return nil })
	}()
	select {
	case err := <-globalResult:
		if err != nil {
			close(releaseStart)
			<-reservationResult
			t.Fatalf("acquire global state lock during VM start: %v", err)
		}
	case <-time.After(time.Second):
		close(releaseStart)
		<-reservationResult
		t.Fatal("USB reservation held the global state lock while starting the VM")
	}
	close(releaseStart)
	if err := <-reservationResult; err != nil {
		t.Fatal(err)
	}
}
