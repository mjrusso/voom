package vm

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
	"github.com/mjrusso/voom/internal/usb"
)

func TestForwardValidationPrecedesPendingRecovery(t *testing.T) {
	for _, command := range []string{"enable", "reconcile", "remove", "ssh-port"} {
		t.Run(command, func(t *testing.T) {
			st, manager, record, _ := newAutoForwardFixture(t)
			image, err := st.LoadImageByID(record.Image.ID)
			if err != nil {
				t.Fatal(err)
			}
			image.Capabilities = state.ImageCapabilities{}
			if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(image.ID), "image.json"), image); err != nil {
				t.Fatal(err)
			}
			path := st.Runtime(record).ForwardMutation()
			if err := os.WriteFile(path, []byte("pending mutation"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			want := "capabilit"
			switch command {
			case "enable":
				enabled := true
				_, err = manager.UpdateAutoForward(ctx, record.Name, AutoForwardUpdate{Enabled: &enabled})
			case "reconcile":
				_, err = manager.ReconcileAutoForwards(ctx, record.Name)
			case "remove":
				err = manager.RemoveForward(ctx, record.Name, 18080, "127.0.0.1", "tcp")
				want = "no tcp forward"
			case "ssh-port":
				_, _, err = manager.SetSSHPort(ctx, record.Name, -1)
				want = "invalid SSH port"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("validation error = %v; want %q", err, want)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "pending mutation" {
				t.Fatalf("validation changed pending mutation: %q %v", data, err)
			}
			if !manager.IsRunning(record) {
				t.Fatal("validation stopped the VM")
			}
		})
	}
}

func TestStartupForwardMutationRetainsUncertainty(t *testing.T) {
	st, m, rec, proxy := newAutoForwardFixture(t)
	port := freeLoopbackPort(t)
	rec.Network.Forwards = []forward.Decl{{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: 8080}}
	if err := st.SaveVM(rec); err != nil {
		t.Fatal(err)
	}
	proxy.dropNextResponses(-1)
	unlock, err := st.TryLockVM(rec.ID)
	if err != nil || unlock == nil {
		t.Fatalf("lock: %v", err)
	}
	err = m.installDeclaredForwardsLocked(context.Background(), rec)
	if !errors.Is(err, gvproxy.ErrForwardOutcomeUnknown) || !errors.Is(err, errForwardRecoveryRequired) || !m.forwardMutationPending(rec) {
		unlock()
		t.Fatalf("startup uncertainty: %v", err)
	}
	_, cleanupErr := m.cleanupRuntime(rec, false)
	unlock()
	if !errors.Is(cleanupErr, errForwardRecoveryRequired) || !m.forwardMutationPending(rec) {
		t.Fatalf("startup cleanup cleared uncertainty: %v", cleanupErr)
	}
	if err := m.RemoveForward(context.Background(), rec.Name, port, "127.0.0.1", "tcp"); err == nil {
		t.Fatal("removal released a reservation before confirmed termination")
	}
	if reserved, err := st.PortReserved("tcp", "127.0.0.1", port); err != nil || !reserved {
		t.Fatalf("startup reservation: %t %v", reserved, err)
	}
	if exposes, unexposes := proxy.counts(); exposes != 1 || unexposes != 0 {
		t.Fatalf("mutations after unknown startup: expose=%d unexpose=%d", exposes, unexposes)
	}
}

func TestShutdownManualForwardMutationRetainsUncertainty(t *testing.T) {
	st, m, rec, proxy := newAutoForwardFixture(t)
	if err := os.WriteFile(st.Runtime(rec).GVProxyProcessRecord(), []byte("invalid identity"), 0600); err != nil {
		t.Fatal(err)
	}
	port := freeLoopbackPort(t)
	rec.Network.Forwards = []forward.Decl{{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: 8080}}
	if err := st.SaveVM(rec); err != nil {
		t.Fatal(err)
	}
	proxy.install(forward.Address("127.0.0.1", port))
	proxy.dropNextResponses(-1)
	if _, err := m.Stop(context.Background(), rec.Name); !errors.Is(err, gvproxy.ErrForwardOutcomeUnknown) {
		t.Fatalf("shutdown uncertainty: %v", err)
	}
	if !m.forwardMutationPending(rec) {
		t.Fatal("unknown shutdown mutation lost its marker")
	}
	proxy.mu.Lock()
	proxy.dropResponses = nil
	proxy.mu.Unlock()
	if err := m.RemoveForward(context.Background(), rec.Name, port, "127.0.0.1", "tcp"); err == nil {
		t.Fatal("removal released a reservation before confirmed termination")
	}
	if reserved, err := st.PortReserved("tcp", "127.0.0.1", port); err != nil || !reserved {
		t.Fatalf("shutdown reservation: %t %v", reserved, err)
	}
	if _, unexposes := proxy.counts(); unexposes != 1 {
		t.Fatalf("retry sent %d unexposes", unexposes)
	}
	gateway := fakeNamedVMProcess(t, "gvproxy")
	writeProcessRecord(t, st.Runtime(rec).GVProxyProcessRecord(), gateway)
	if _, err := m.Stop(context.Background(), rec.Name); err != nil {
		t.Fatal(err)
	}
	if m.forwardMutationPending(rec) {
		t.Fatal("confirmed termination retained its marker")
	}
	if err := m.RemoveForward(context.Background(), rec.Name, port, "127.0.0.1", "tcp"); err != nil {
		t.Fatal(err)
	}
	if reserved, err := st.PortReserved("tcp", "127.0.0.1", port); err != nil || reserved {
		t.Fatalf("reservation after confirmed termination: %t %v", reserved, err)
	}
}

func TestAutoForwardMutationRecovery(t *testing.T) {
	for _, failure := range []string{"lost-response", "timeout", "resolved"} {
		for _, action := range []string{"expose", "unexpose"} {
			t.Run(failure+"/"+action, func(t *testing.T) {
				st, _, rec, proxy := newAutoForwardFixture(t)
				recorder := &recordingEmitter{}
				m := New(st, WithEventEmitter(recorder))
				gateway := fakeNamedVMProcess(t, "gvproxy")
				writeProcessRecord(t, st.Runtime(rec).GVProxyProcessRecord(), gateway)
				if failure == "resolved" {
					proxy.dropNextResponses(1)
				} else {
					proxy.dropNextResponses(-1)
				}
				if failure == "timeout" {
					proxy.waitResponses = map[string]bool{"1": true}
				}
				port := freeLoopbackPort(t)
				local := forward.Address("127.0.0.1", port)
				report := GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now()}
				if action == "expose" {
					report.Listeners = []GuestListener{{Proto: "tcp", Addr: "0.0.0.0", Port: port}}
				} else {
					proxy.install(local)
					row := RuntimeAutoForward{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: port, Status: "active"}
					if err := m.saveRuntimeAutoForwards(rec, []RuntimeAutoForward{row}); err != nil {
						t.Fatal(err)
					}
				}
				writeAutoForwardReport(t, st, rec, report)
				monitor, err := net.Listen("unix", st.Runtime(rec).QEMUMonitor())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = monitor.Close() }()
				contacted := make(chan struct{})
				go func() {
					conn, acceptErr := monitor.Accept()
					if acceptErr == nil {
						close(contacted)
						_ = conn.Close()
					}
				}()
				_, err = m.ReconcileAutoForwards(context.Background(), rec.Name)
				lastErr := ""
				if err != nil {
					lastErr = err.Error()
				}
				if (err == nil) != (failure == "resolved") {
					t.Fatalf("reconciliation: %v", err)
				}
				if !m.IsRunning(rec) {
					t.Fatal("forwarding recovery stopped guest")
				}
				if _, ok := process.ValidRecord(st.Runtime(rec).GVProxyProcessRecord(), "gvproxy"); !ok {
					t.Fatal("forwarding recovery stopped gvproxy")
				}
				select {
				case <-contacted:
					t.Fatal("forwarding recovery contacted QMP for system_powerdown")
				default:
				}
				assertVMStopEvents(t, recorder, rec.ID, 0)
				if failure == "resolved" {
					rows, readErr := m.ReadRuntimeAutoForwards(rec)
					if readErr != nil || m.forwardMutationPending(rec) {
						t.Fatalf("resolved journal: %v", readErr)
					}
					_, installed := proxy.remote(local)
					if action == "expose" {
						if !installed || len(rows) != 1 || !rows[0].Installed() {
							t.Fatalf("resolved expose: %#v", rows)
						}
					} else if installed || len(rows) != 0 {
						t.Fatalf("resolved unexpose: %#v", rows)
					}
					return
				}
				if !errors.Is(err, gvproxy.ErrForwardOutcomeUnknown) || !m.forwardMutationPending(rec) {
					t.Fatalf("uncertainty lost: %v", err)
				}
				if reserved, reserveErr := st.PortReserved("tcp", "127.0.0.1", port); reserveErr != nil || !reserved {
					t.Fatalf("reservation: %t %v", reserved, reserveErr)
				}
				outcome, err := m.watchRuntimeOnce(context.Background(), rec.ID, "", &runtimeWatchSchedule{})
				m.logForwardWatchResult(rec, err, lastErr)
				if outcome == runtimeWatchSkipped || !errors.Is(err, errForwardRecoveryRequired) {
					t.Fatalf("retry: %d %v", outcome, err)
				}
				if len(recorder.events) == 0 || recorder.events[0].Type != "forward" || recorder.events[0].Action != "recovery" {
					t.Fatalf("recovery events: %#v", recorder.events)
				}
				attributes := recorder.events[0].Actor.Attributes
				if attributes["operation"] != action || attributes["endpoint"] != local || attributes["error"] == "" || attributes["outcome"] != "degraded" {
					t.Fatalf("recovery context: %#v", attributes)
				}
				log, logErr := os.ReadFile(st.LogPath(rec, "auto-forward"))
				if logErr != nil || !strings.Contains(string(log), attributes["error"]) {
					t.Fatalf("recovery log: %q %v", log, logErr)
				}
			})
		}
	}
}

func TestLostAutoForwardRemovalRetainsUncertainty(t *testing.T) {
	st, m, rec, proxy := newAutoForwardFixture(t)
	port := freeLoopbackPort(t)
	local := forward.Address("127.0.0.1", port)
	row := RuntimeAutoForward{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: port, GuestTargetIP: m.GuestTargetIP(rec), Status: "active"}
	proxy.install(local)
	proxy.dropNextResponses(-1)
	if err := m.saveRuntimeAutoForwards(rec, []RuntimeAutoForward{row}); err != nil {
		t.Fatal(err)
	}
	writeAutoForwardReport(t, st, rec, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now()})
	if _, err := m.ReconcileAutoForwards(context.Background(), rec.Name); err == nil {
		t.Fatal("lost removal response reported success")
	}
	rows, err := m.ReadRuntimeAutoForwards(rec)
	if err != nil || len(rows) != 1 || !rows[0].Installed() || rows[0].Status != "active" || !m.forwardMutationPending(rec) {
		t.Fatalf("lost removal uncertainty: %#v %v", rows, err)
	}
	if _, exists := proxy.remote(local); exists {
		t.Fatal("backend did not remove listener before losing response")
	}
	writeAutoForwardReport(t, st, rec, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now(), Listeners: []GuestListener{{Proto: "tcp", Addr: "0.0.0.0", Port: port}}})
	if _, err := m.ReconcileAutoForwards(context.Background(), rec.Name); err == nil {
		t.Fatal("retry reused an uncertain installation")
	}
	reserved, err := st.PortReserved("tcp", "0.0.0.0", port)
	if err != nil || !reserved {
		t.Fatalf("lost removal reservation: %t %v", reserved, err)
	}
	if exposes, unexposes := proxy.counts(); exposes != 0 || unexposes != 1 {
		t.Fatalf("retry sent mutations: expose=%d unexpose=%d", exposes, unexposes)
	}
}

func TestAutoForwardJournalSaveFailureKeepsRuntimeAlive(t *testing.T) {
	st, m, rec, proxy := newAutoForwardFixture(t)
	gateway := fakeNamedVMProcess(t, "gvproxy")
	writeProcessRecord(t, st.Runtime(rec).GVProxyProcessRecord(), gateway)
	unlock, err := st.TryLockVM(rec.ID)
	if err != nil || unlock == nil {
		t.Fatalf("lock: %v", err)
	}
	defer unlock()
	if os.Geteuid() == 0 {
		t.Skip("requires filesystem permission enforcement")
	}
	if err := os.Chmod(st.Runtime(rec).Dir(), 0500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(st.Runtime(rec).Dir(), 0700) }()
	port := freeLoopbackPort(t)
	desired := []forward.AutoPlan{{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: port}}
	if _, err := m.installAutoForwardRowsLocked(context.Background(), rec, desired, nil); err == nil {
		t.Fatal("reservation save succeeded")
	}
	if !m.IsRunning(rec) {
		t.Fatal("reservation save failure stopped the VM")
	}
	if _, ok := process.ValidRecord(st.Runtime(rec).GVProxyProcessRecord(), "gvproxy"); !ok {
		t.Fatal("reservation save failure stopped gvproxy")
	}
	if exposes, unexposes := proxy.counts(); exposes != 0 || unexposes != 0 {
		t.Fatalf("backend mutations: %d %d", exposes, unexposes)
	}
	if _, err := os.Stat(st.Runtime(rec).ForwardMutation()); !os.IsNotExist(err) {
		t.Fatalf("mutation marker: %v", err)
	}
}

func TestAutoForwardExposeRejectionKeepsRuntimeAlive(t *testing.T) {
	st, m, rec, proxy := newAutoForwardFixture(t)
	proxy.exposeStatus = 409
	port := freeLoopbackPort(t)
	writeAutoForwardReport(t, st, rec, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now(), Listeners: []GuestListener{{Proto: "tcp", Addr: "0.0.0.0", Port: port}}})
	rows, err := m.ReconcileAutoForwards(context.Background(), rec.Name)
	if err != nil || len(rows) != 1 || rows[0].Installed() || rows[0].Status != "skipped" {
		t.Fatalf("rejected expose: %#v %v", rows, err)
	}
	if !m.IsRunning(rec) {
		t.Fatal("rejected expose stopped the VM")
	}
	if reserved, err := st.PortReserved("tcp", "127.0.0.1", port); err != nil || reserved {
		t.Fatalf("rejected reservation: %t %v", reserved, err)
	}
	if _, err := os.Stat(st.Runtime(rec).ForwardMutation()); !os.IsNotExist(err) {
		t.Fatalf("mutation marker: %v", err)
	}
}

func exitAutoForwardFixtureDriver(t *testing.T, st *state.Store, rec *state.VMRecord) {
	t.Helper()
	pid, ok := process.ValidRecord(st.Runtime(rec).VMProcessRecord(), state.VMProcessKind(rec.Driver))
	if !ok {
		t.Fatal("fixture driver is not running")
	}
	driver, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Kill(); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestForwardRecoveryWaitsForMacStorageAndReportsExit(t *testing.T) {
	st, rec, _ := macosResetFixture(t)
	recorder := &recordingEmitter{}
	m := New(st, WithEventEmitter(recorder))
	rt := st.Runtime(rec)
	driver := fakeNamedVMProcess(t, "vfkit")
	writeProcessRecord(t, rt.VMProcessRecord(), driver)
	fingerprint, err := process.RecordFingerprint(rt.VMProcessRecord())
	if err != nil {
		t.Fatal(err)
	}
	writeProcessRecord(t, rt.GVProxyProcessRecord(), fakeNamedVMProcess(t, "gvproxy"))
	writeProcessRecord(t, rt.RuntimeWatcherProcessRecord(), fakeNamedVMProcess(t, "voom-test", "debug", "watch-vm", rec.ID, fingerprint))
	if err := driver.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Process.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSONAtomic(rt.ForwardMutation(), []forward.Decl{{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 8080, GuestPort: 8080}}); err != nil {
		t.Fatal(err)
	}
	release := holdMacOSStorageLock(t, filepath.Join(st.VMStorageDir(rec), "AuxiliaryStorage"))
	unlock, err := st.TryLockVM(rec.ID)
	if err != nil || unlock == nil {
		t.Fatalf("VM lock: %v", err)
	}
	defer unlock()
	result := make(chan error, 1)
	completed := make(chan struct{})
	go func() { defer close(completed); result <- m.recoverPendingForwardMutation(context.Background(), rec) }()
	t.Cleanup(func() { release(); <-completed })
	if err := waitFor(context.Background(), func() bool { return !m.forwardMutationPending(rec) }, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	var recoveryErr error
	returned := false
	select {
	case recoveryErr = <-result:
		returned = true
		t.Error("forwarding recovery returned while MacOS storage was locked")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	if !returned {
		recoveryErr = <-result
	}
	if !errors.Is(recoveryErr, errForwardRecoveryRequired) {
		t.Fatalf("recovery error: %v", recoveryErr)
	}
	assertVMStopEvents(t, recorder, rec.ID, 1)
	for _, event := range recorder.events {
		if event.Type == "vm" && event.Action == "stop" && event.Actor.Attributes["reason"] != "driver exited" {
			t.Errorf("stop reason: %#v", event)
		}
	}
	if _, err := m.watchRuntimeOnce(context.Background(), rec.ID, fingerprint, &runtimeWatchSchedule{}); err != nil {
		t.Fatal(err)
	}
	assertVMStopEvents(t, recorder, rec.ID, 1)
}

func TestPendingForwardRecoveryRecordsActualFailure(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "invalid-identity"}[malformed], func(t *testing.T) {
			st, _, rec, _ := newAutoForwardFixture(t)
			recorder := &recordingEmitter{}
			m := New(st, WithEventEmitter(recorder))
			rt := st.Runtime(rec)
			if err := state.WriteJSONAtomic(rt.ForwardMutation(), []forward.Decl{{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 8080, GuestPort: 8080}}); err != nil {
				t.Fatal(err)
			}
			if malformed {
				if err := os.WriteFile(rt.VMProcessRecord(), []byte("invalid identity"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for attempt := range 2 {
				_, err := m.ReconcileAutoForwards(context.Background(), rec.Name)
				if !errors.Is(err, errForwardRecoveryRequired) {
					t.Fatalf("recovery error: %v", err)
				}
				if len(recorder.events) != attempt+1 {
					t.Fatalf("recovery diagnostics missing: %#v", recorder.events)
				}
				event := recorder.events[attempt]
				if event.Type != "forward" || event.Action != "recovery" || event.Actor.Attributes["outcome"] != "degraded" || event.Actor.Attributes["error"] != err.Error() {
					t.Fatalf("recorded failure: %#v, actual: %v", event, err)
				}
				log, logErr := os.ReadFile(st.LogPath(rec, "auto-forward"))
				if logErr != nil || !strings.Contains(string(log), err.Error()) || strings.Count(string(log), "forward recovery degraded:") != attempt+1 {
					t.Fatalf("recovery log: %q %v", log, logErr)
				}
			}
		})
	}
}

func TestWatcherDeduplicatesPendingRecoveryObservations(t *testing.T) {
	st, m, rec, _ := newAutoForwardFixture(t)
	recorder := &recordingEmitter{}
	m.events = recorder
	rt := st.Runtime(rec)
	identity, err := os.ReadFile(rt.VMProcessRecord())
	if err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSONAtomic(rt.ForwardMutation(), []forward.Decl{{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 8080, GuestPort: 8080}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.VMProcessRecord(), []byte("invalid identity"), 0600); err != nil {
		t.Fatal(err)
	}
	lastErr := ""
	for phase := range 2 {
		if phase == 1 {
			if err := os.WriteFile(rt.VMProcessRecord(), identity, 0600); err != nil {
				t.Fatal(err)
			}
		}
		for range 2 {
			_, err := m.watchRuntimeOnce(context.Background(), rec.ID, "", &runtimeWatchSchedule{})
			lastErr = m.logForwardWatchResult(rec, err, lastErr)
		}
		if len(recorder.events) != phase+1 {
			t.Fatalf("watcher recovery diagnostics: %#v", recorder.events)
		}
		if recorder.events[phase].Actor.Attributes["error"] != lastErr {
			t.Fatalf("recorded failure omits observation: %#v, actual: %s", recorder.events[phase], lastErr)
		}
	}
	log, err := os.ReadFile(st.LogPath(rec, "auto-forward"))
	if err != nil || strings.Count(string(log), "forward recovery degraded:") != 2 {
		t.Fatalf("duplicate or missing recovery logs: %q %v", log, err)
	}
}

func TestExitCleanupRetainsReservationsUntilGVProxyStops(t *testing.T) {
	st, _, rec, _ := newAutoForwardFixture(t)
	if err := os.WriteFile(st.Runtime(rec).GVProxyProcessRecord(), []byte("invalid identity"), 0600); err != nil {
		t.Fatal(err)
	}
	recorder := &recordingEmitter{}
	m := New(st, WithEventEmitter(recorder))
	rt := st.Runtime(rec)
	device := usb.Decl{Name: "board", Route: usb.Route{Controller: "test-controller", Protocol: 2, Port: "255"}}
	rec.USBDevices = []usb.Decl{device}
	if err := st.SaveVM(rec); err != nil {
		t.Fatal(err)
	}
	exitAutoForwardFixtureDriver(t, st, rec)
	monitor, err := net.Listen("unix", rt.QEMUMonitor())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = monitor.Close() }()
	other := &state.VMRecord{ID: "other"}
	if err := m.checkUSBConflicts(other, []usb.Decl{device}); err == nil {
		t.Fatal("stale runtime did not reserve USB device")
	}
	row := forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 8080, GuestPort: 8080}
	if err := state.WriteJSONAtomic(rt.ForwardMutation(), []forward.Decl{row}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		outcome, err := m.watchRuntimeOnce(context.Background(), rec.ID, "", &runtimeWatchSchedule{})
		if outcome == runtimeWatchSkipped || !errors.Is(err, errForwardRecoveryRequired) {
			t.Fatalf("unconfirmed recovery: outcome=%d error=%v", outcome, err)
		}
		assertVMStopEvents(t, recorder, rec.ID, 0)
		if !process.HasRecord(rt.VMProcessRecord()) {
			t.Fatal("exit evidence removed before gateway termination was confirmed")
		}
		if reserved, err := st.PortReserved("tcp", row.Bind, row.HostPort); err != nil || !reserved {
			t.Fatalf("unconfirmed reservation: %t %v", reserved, err)
		}
	}
	gateway := fakeNamedVMProcess(t, "gvproxy")
	writeProcessRecord(t, rt.GVProxyProcessRecord(), gateway)
	outcome, err := m.watchRuntimeOnce(context.Background(), rec.ID, "", &runtimeWatchSchedule{})
	if outcome != runtimeWatchDone || err != nil {
		t.Fatalf("confirmed recovery: outcome=%d error=%v", outcome, err)
	}
	assertVMStopEvents(t, recorder, rec.ID, 1)
	if m.forwardMutationPending(rec) {
		t.Fatal("confirmed recovery retained mutation marker")
	}
	if reserved, err := st.PortReserved("tcp", row.Bind, row.HostPort); err != nil || reserved {
		t.Fatalf("confirmed reservation: %t %v", reserved, err)
	}
	if err := m.checkUSBConflicts(other, []usb.Decl{device}); err != nil {
		t.Errorf("recovery retained USB reservation: %v", err)
	}
	for _, path := range []string{rt.VMProcessRecord(), rt.QEMUMonitor()} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("recovery retained driver artifact %s: %v", path, err)
		}
	}
}

func TestForwardRecoveryRetainsUnverifiableRuntime(t *testing.T) {
	for _, damage := range []string{"malformed", "unreadable", "missing", "mismatched"} {
		t.Run(damage, func(t *testing.T) {
			if damage == "unreadable" && os.Geteuid() == 0 {
				t.Skip("requires filesystem permission enforcement")
			}
			st, _, rec, proxy := newAutoForwardFixture(t)
			recorder := &recordingEmitter{}
			m := New(st, WithEventEmitter(recorder))
			rt := st.Runtime(rec)
			pid, ok := process.ValidRecord(rt.VMProcessRecord(), state.VMProcessKind(rec.Driver))
			if !ok {
				t.Fatal("fixture driver is not running")
			}
			gateway := fakeNamedVMProcess(t, "gvproxy")
			writeProcessRecord(t, rt.GVProxyProcessRecord(), gateway)
			fs := rt.Virtiofsd("data")
			writer := fakeNamedVMProcess(t, "virtiofsd")
			writeProcessRecord(t, fs.ProcessRecord, writer)
			port := freeLoopbackPort(t)
			row := RuntimeAutoForward{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: port, Status: "active"}
			proxy.install(forward.Address(row.Bind, port))
			if err := m.saveRuntimeAutoForwards(rec, []RuntimeAutoForward{row}); err != nil {
				t.Fatal(err)
			}
			if err := state.WriteJSONAtomic(rt.ForwardMutation(), []forward.Decl{row.Endpoint()}); err != nil {
				t.Fatal(err)
			}
			var err error
			switch damage {
			case "malformed":
				err = os.WriteFile(rt.VMProcessRecord(), []byte("invalid identity"), 0600)
			case "unreadable":
				err = os.Chmod(rt.VMProcessRecord(), 0)
				defer func() { _ = os.Chmod(rt.VMProcessRecord(), 0600) }()
			case "missing":
				err = os.Remove(rt.VMProcessRecord())
			case "mismatched":
				err = process.Record(rt.VMProcessRecord(), gateway.Process.Pid)
			}
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				outcome, recoveryErr := m.watchRuntimeOnce(context.Background(), rec.ID, "", &runtimeWatchSchedule{})
				if outcome == runtimeWatchSkipped || !errors.Is(recoveryErr, errForwardRecoveryRequired) {
					t.Errorf("recovery: %d %v", outcome, recoveryErr)
				}
			}
			if !process.Alive(pid) {
				t.Error("unverifiable driver was stopped")
			}
			for _, helper := range []struct{ record, kind string }{{rt.GVProxyProcessRecord(), "gvproxy"}, {fs.ProcessRecord, "virtiofsd"}} {
				if _, ok := process.ValidRecord(helper.record, helper.kind); !ok {
					t.Errorf("recovery stopped %s", helper.kind)
				}
			}
			if !m.forwardMutationPending(rec) {
				t.Error("recovery cleared journal without confirmed driver exit")
			}
			if reserved, err := st.PortReserved("tcp", row.Bind, port); err != nil || !reserved {
				t.Errorf("reservation: %t %v", reserved, err)
			}
			rows, err := m.ReadRuntimeAutoForwards(rec)
			if err != nil || len(rows) != 1 || !rows[0].Installed() {
				t.Errorf("runtime forwards: %#v %v", rows, err)
			}
			for _, event := range recorder.events {
				if event.Action == "recovery" && event.Actor.Attributes["outcome"] == "cleanup-after-exit" {
					t.Error("unverified identity reported as driver exit")
				}
			}
		})
	}
}

func TestShutdownRetainsAutoReservationAfterLostRemovalResponse(t *testing.T) {
	st, m, rec, proxy := newAutoForwardFixture(t)
	if err := os.WriteFile(st.Runtime(rec).GVProxyProcessRecord(), []byte("invalid identity"), 0600); err != nil {
		t.Fatal(err)
	}
	port := freeLoopbackPort(t)
	row := RuntimeAutoForward{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: port, Status: "active"}
	proxy.install(forward.Address(row.Bind, port))
	proxy.dropNextResponses(-1)
	if err := m.saveRuntimeAutoForwards(rec, []RuntimeAutoForward{row}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop(context.Background(), rec.Name); err == nil {
		t.Fatal("unconfirmed backend termination reported success")
	}
	rows, err := m.ReadRuntimeAutoForwards(rec)
	if err != nil || len(rows) != 1 || rows[0].Status != "active" || !m.forwardMutationPending(rec) {
		t.Fatalf("shutdown reservation: %#v %v", rows, err)
	}
	reserved, err := st.PortReserved("tcp", "0.0.0.0", port)
	if err != nil || !reserved {
		t.Fatalf("shutdown released reservation: %t %v", reserved, err)
	}
	if exposes, unexposes := proxy.counts(); exposes != 0 || unexposes != 1 {
		t.Fatalf("shutdown mutations: %d %d", exposes, unexposes)
	}
	gateway := fakeNamedVMProcess(t, "gvproxy")
	writeProcessRecord(t, st.Runtime(rec).GVProxyProcessRecord(), gateway)
	if _, err := m.Stop(context.Background(), rec.Name); err != nil {
		t.Fatal(err)
	}
	rows, err = m.ReadRuntimeAutoForwards(rec)
	if err != nil || len(rows) != 0 {
		t.Fatalf("confirmed termination retained reservation: %#v %v", rows, err)
	}
}

func TestAutoForwardCommandsRecoverUncertainRemoval(t *testing.T) {
	for _, command := range []string{"reconcile", "disable", "offset"} {
		t.Run(command, func(t *testing.T) {
			st, _, rec, proxy := newAutoForwardFixture(t)
			recorder := &recordingEmitter{}
			m := New(st, WithEventEmitter(recorder))
			m.startSelfRecorded = func([]string, string, string) error {
				return nil
			}
			gateway := fakeNamedVMProcess(t, "gvproxy")
			writeProcessRecord(t, st.Runtime(rec).GVProxyProcessRecord(), gateway)
			port := freeLoopbackPort(t)
			row := RuntimeAutoForward{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: port, Status: forward.ReservationActive}
			proxy.install(forward.Address(row.Bind, port))
			proxy.dropNextResponses(-1)
			if err := m.saveRuntimeAutoForwards(rec, []RuntimeAutoForward{row}); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			var err error
			switch command {
			case "reconcile":
				writeAutoForwardReport(t, st, rec, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now()})
				var rows []RuntimeAutoForward
				rows, err = m.ReconcileAutoForwards(ctx, rec.Name)
				if len(rows) != 1 {
					t.Fatalf("recovery lost reservations: %#v", rows)
				}
			case "disable":
				enabled := false
				_, err = m.UpdateAutoForward(ctx, rec.Name, AutoForwardUpdate{Enabled: &enabled})
			case "offset":
				offset := 1
				_, err = m.UpdateAutoForward(ctx, rec.Name, AutoForwardUpdate{Offset: &offset})
			}
			if !errors.Is(err, gvproxy.ErrForwardOutcomeUnknown) {
				t.Fatalf("uncertain removal: %v", err)
			}
			if !m.IsRunning(rec) || !process.HasRecord(st.Runtime(rec).GVProxyProcessRecord()) {
				t.Fatal("recovery stopped the runtime")
			}
			assertVMStopEvents(t, recorder, rec.ID, 0)
			if reserved, err := st.PortReserved("tcp", row.Bind, port); err != nil || !reserved {
				t.Fatalf("degraded recovery lost reservation: %t %v", reserved, err)
			}
		})
	}
}

func TestSSHPortChangeRecoversUncertainStartup(t *testing.T) {
	st, m, rec, proxy := newAutoForwardFixture(t)
	if err := os.WriteFile(st.Runtime(rec).GVProxyProcessRecord(), []byte("invalid identity"), 0600); err != nil {
		t.Fatal(err)
	}
	rec.Network.SSHBind = "127.0.0.1"
	rec.Network.SSHPort = freeLoopbackPort(t)
	if err := st.SaveVM(rec); err != nil {
		t.Fatal(err)
	}
	unlock, err := st.TryLockVM(rec.ID)
	if err != nil || unlock == nil {
		t.Fatalf("VM lock: %v", err)
	}
	proxy.dropNextResponses(-1)
	err = m.installDeclaredForwardsLocked(context.Background(), rec)
	unlock()
	if !errors.Is(err, gvproxy.ErrForwardOutcomeUnknown) {
		t.Fatal(err)
	}
	exitAutoForwardFixtureDriver(t, st, rec)
	newPort := freeLoopbackPort(t)
	if _, _, err := m.SetSSHPort(context.Background(), rec.Name, newPort); !errors.Is(err, errForwardRecoveryRequired) {
		t.Fatalf("unconfirmed SSH change: %v", err)
	}
	if reserved, err := st.PortReserved("tcp", rec.Network.SSHBind, rec.Network.SSHPort); err != nil || !reserved {
		t.Fatalf("uncertain SSH reservation: %t %v", reserved, err)
	}
	if saved, err := st.LoadVM(rec.Name); err != nil || saved.Network.SSHPort != rec.Network.SSHPort {
		t.Fatalf("unconfirmed SSH change persisted: %#v %v", saved, err)
	}
	replacement := *rec
	replacement.Network.SSHPort = newPort
	if err := st.SaveVM(&replacement); err != nil {
		t.Fatal(err)
	}
	if reserved, err := st.PortReserved("tcp", rec.Network.SSHBind, rec.Network.SSHPort); err != nil || !reserved {
		t.Fatalf("mutation reservation depends on SSH declaration: %t %v", reserved, err)
	}
	if err := st.SaveVM(rec); err != nil {
		t.Fatal(err)
	}
	gateway := fakeNamedVMProcess(t, "gvproxy")
	writeProcessRecord(t, st.Runtime(rec).GVProxyProcessRecord(), gateway)
	if _, _, err := m.SetSSHPort(context.Background(), rec.Name, newPort); !errors.Is(err, errForwardRecoveryRequired) || m.forwardMutationPending(rec) {
		t.Fatalf("confirmed SSH recovery: %v", err)
	}
	if _, changed, err := m.SetSSHPort(context.Background(), rec.Name, newPort); err != nil || !changed {
		t.Fatalf("SSH change after recovery: %t %v", changed, err)
	}
	if reserved, err := st.PortReserved("tcp", rec.Network.SSHBind, rec.Network.SSHPort); err != nil || reserved {
		t.Fatalf("old SSH reservation after recovery: %t %v", reserved, err)
	}
}

func TestStoppedAutoForwardCommandsRecoverPendingMutation(t *testing.T) {
	for _, command := range []string{"disable", "enable", "offset", "reconcile"} {
		t.Run(command, func(t *testing.T) {
			st, m, rec, proxy := newAutoForwardFixture(t)
			if err := os.WriteFile(st.Runtime(rec).GVProxyProcessRecord(), []byte("invalid identity"), 0600); err != nil {
				t.Fatal(err)
			}
			exitAutoForwardFixtureDriver(t, st, rec)
			row := forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 8080, GuestPort: 8080}
			if err := state.WriteJSONAtomic(st.Runtime(rec).ForwardMutation(), []forward.Decl{row}); err != nil {
				t.Fatal(err)
			}
			run := func() error {
				if command == "reconcile" {
					_, err := m.ReconcileAutoForwards(context.Background(), rec.Name)
					return err
				}
				update := AutoForwardUpdate{}
				if command == "offset" {
					offset := 1
					update.Offset = &offset
				} else {
					enabled := command == "enable"
					update.Enabled = &enabled
				}
				_, err := m.UpdateAutoForward(context.Background(), rec.Name, update)
				return err
			}
			if err := run(); !errors.Is(err, errForwardRecoveryRequired) || !m.forwardMutationPending(rec) {
				t.Fatalf("unconfirmed recovery: %v", err)
			}
			if saved, err := st.LoadVM(rec.Name); err != nil || saved.Network.AutoForward != rec.Network.AutoForward || saved.Network.AutoForwardHostOffset != rec.Network.AutoForwardHostOffset {
				t.Fatalf("settings changed before recovery: %#v %v", saved, err)
			}
			if reserved, err := st.PortReserved(row.Protocol, row.Bind, row.HostPort); err != nil || !reserved {
				t.Fatalf("unconfirmed reservation: %t %v", reserved, err)
			}
			gateway := fakeNamedVMProcess(t, "gvproxy")
			writeProcessRecord(t, st.Runtime(rec).GVProxyProcessRecord(), gateway)
			if err := run(); !errors.Is(err, errForwardRecoveryRequired) || m.forwardMutationPending(rec) {
				t.Fatalf("confirmed recovery: %v", err)
			}
			if reserved, err := st.PortReserved(row.Protocol, row.Bind, row.HostPort); err != nil || reserved {
				t.Fatalf("confirmed reservation: %t %v", reserved, err)
			}
			if exposes, unexposes := proxy.counts(); exposes != 0 || unexposes != 0 {
				t.Fatalf("recovery sent forwarding requests: %d %d", exposes, unexposes)
			}
		})
	}
}
