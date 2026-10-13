package vm

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

func TestStartupForwardMutationRetainsUncertainty(t *testing.T) {
	st, m, rec, proxy := newAutoForwardFixture(t)
	port := freeLoopbackPort(t)
	rec.Network.Forwards = []forward.Decl{{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: 8080}}
	if err := st.SaveVM(rec); err != nil {
		t.Fatal(err)
	}
	proxy.loseExposeResponse = true
	unlock, err := st.TryLockVM(rec.ID)
	if err != nil || unlock == nil {
		t.Fatalf("lock: %v", err)
	}
	err = m.installDeclaredForwardsLocked(rec)
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
	port := freeLoopbackPort(t)
	rec.Network.Forwards = []forward.Decl{{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: 8080}}
	if err := st.SaveVM(rec); err != nil {
		t.Fatal(err)
	}
	proxy.install(forward.Address("127.0.0.1", port))
	proxy.loseUnexposeResponse = true
	if _, err := m.Stop(context.Background(), rec.Name); !errors.Is(err, gvproxy.ErrForwardOutcomeUnknown) {
		t.Fatalf("shutdown uncertainty: %v", err)
	}
	if !m.forwardMutationPending(rec) {
		t.Fatal("unknown shutdown mutation lost its marker")
	}
	proxy.mu.Lock()
	proxy.loseUnexposeResponse = false
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

func TestAutoForwardWatcherRecoversLostResponse(t *testing.T) {
	if os.Getenv("VOOM_WATCHER_RECOVERY_CHILD") == "1" {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer cancel()
		st, err := state.Open()
		if err != nil {
			t.Fatal(err)
		}
		rec, err := st.LoadVM("demo")
		if err != nil {
			t.Fatal(err)
		}
		if err := process.Record(st.Runtime(rec).AutoForwardProcessRecord(), os.Getpid()); err != nil {
			t.Fatal(err)
		}
		recorder := &recordingEmitter{}
		if err := New(st, WithEventEmitter(recorder)).WatchAutoForwards(ctx, rec.ID); err == nil {
			t.Fatal("lost response reported success")
		}
		assertVMStopEvents(t, recorder, rec.ID, 1)
		return
	}
	for _, action := range []string{"expose", "unexpose"} {
		t.Run(action, func(t *testing.T) {
			st, m, rec, proxy := newAutoForwardFixture(t)
			if action == "expose" {
				proxy.loseExposeResponse = true
			} else {
				proxy.loseUnexposeResponse = true
			}
			gateway := fakeNamedVMProcess(t, "gvproxy")
			writeProcessRecord(t, st.Runtime(rec).GVProxyProcessRecord(), gateway)
			port := freeLoopbackPort(t)
			report := GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now()}
			if action == "expose" {
				report.Listeners = []GuestListener{{Proto: "tcp", Addr: "0.0.0.0", Port: port}}
			} else {
				proxy.install(forward.Address("127.0.0.1", port))
				row := RuntimeAutoForward{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: port, Status: "active"}
				if err := m.saveRuntimeAutoForwards(rec, []RuntimeAutoForward{row}); err != nil {
					t.Fatal(err)
				}
			}
			writeAutoForwardReport(t, st, rec, report)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, exe, "-test.run=^TestAutoForwardWatcherRecoversLostResponse$", "--", "forward", "auto", "watch", rec.ID)
			child.Env = append(os.Environ(), "VOOM_WATCHER_RECOVERY_CHILD=1")
			if output, err := child.CombinedOutput(); err != nil {
				t.Fatalf("watcher recovery: %v\n%s", err, output)
			}
			if m.IsRunning(rec) || process.HasRecord(st.Runtime(rec).GVProxyProcessRecord()) || process.HasRecord(st.Runtime(rec).AutoForwardProcessRecord()) {
				t.Fatal("watcher recovery left runtime processes or records")
			}
			rows, err := m.ReadRuntimeAutoForwards(rec)
			if err != nil || len(rows) != 0 {
				t.Fatalf("confirmed termination left reservations: %#v %v", rows, err)
			}
		})
	}
}

func TestLostAutoForwardRemovalRetainsUncertainty(t *testing.T) {
	st, m, rec, proxy := newAutoForwardFixture(t)
	port := freeLoopbackPort(t)
	local := forward.Address("127.0.0.1", port)
	row := RuntimeAutoForward{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: port, GuestTargetIP: m.GuestTargetIP(rec), Status: "active"}
	proxy.install(local)
	proxy.loseUnexposeResponse = true
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
	if _, err := m.installAutoForwardRowsLocked(rec, desired, nil); err == nil {
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

func TestAutoForwardWatcherRetriesRecoveryAfterDriverExit(t *testing.T) {
	st, _, rec, _ := newAutoForwardFixture(t)
	recorder := &recordingEmitter{}
	m := New(st, WithEventEmitter(recorder))
	rt := st.Runtime(rec)
	if err := process.StopRecorded(rt.VMProcessRecord(), state.VMProcessKind(rec.Driver), processStopTimeout); err != nil {
		t.Fatal(err)
	}
	row := forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 8080, GuestPort: 8080}
	if err := state.WriteJSONAtomic(rt.ForwardMutation(), []forward.Decl{row}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		outcome, err := m.watchAutoForwardsOnce(rec.ID)
		if outcome != autoForwardWatchContinue || !errors.Is(err, errForwardRecoveryRequired) {
			t.Fatalf("unconfirmed recovery: outcome=%d error=%v", outcome, err)
		}
		assertVMStopEvents(t, recorder, rec.ID, 0)
		if reserved, err := st.PortReserved("tcp", row.Bind, row.HostPort); err != nil || !reserved {
			t.Fatalf("unconfirmed reservation: %t %v", reserved, err)
		}
	}
	gateway := fakeNamedVMProcess(t, "gvproxy")
	writeProcessRecord(t, rt.GVProxyProcessRecord(), gateway)
	outcome, err := m.watchAutoForwardsOnce(rec.ID)
	if outcome != autoForwardWatchDone || !errors.Is(err, errForwardRecoveryRequired) {
		t.Fatalf("confirmed recovery: outcome=%d error=%v", outcome, err)
	}
	assertVMStopEvents(t, recorder, rec.ID, 1)
	if m.forwardMutationPending(rec) {
		t.Fatal("confirmed recovery retained mutation marker")
	}
	if reserved, err := st.PortReserved("tcp", row.Bind, row.HostPort); err != nil || reserved {
		t.Fatalf("confirmed reservation: %t %v", reserved, err)
	}
}

func TestShutdownRetainsAutoReservationAfterLostRemovalResponse(t *testing.T) {
	st, m, rec, proxy := newAutoForwardFixture(t)
	port := freeLoopbackPort(t)
	row := RuntimeAutoForward{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: port, Status: "active"}
	proxy.install(forward.Address(row.Bind, port))
	proxy.loseUnexposeResponse = true
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
				t.Fatal("recovery started a watcher for the stopped VM")
				return nil
			}
			gateway := fakeNamedVMProcess(t, "gvproxy")
			writeProcessRecord(t, st.Runtime(rec).GVProxyProcessRecord(), gateway)
			port := freeLoopbackPort(t)
			row := RuntimeAutoForward{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: port, Status: forward.ReservationActive}
			proxy.install(forward.Address(row.Bind, port))
			proxy.loseUnexposeResponse = true
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
				if len(rows) != 0 {
					t.Fatalf("recovery returned stale reservations: %#v", rows)
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
			if m.IsRunning(rec) || process.HasRecord(st.Runtime(rec).GVProxyProcessRecord()) {
				t.Fatal("recovery left the runtime alive")
			}
			assertVMStopEvents(t, recorder, rec.ID, 1)
			if reserved, err := st.PortReserved("tcp", row.Bind, port); err != nil || reserved {
				t.Fatalf("confirmed recovery retained reservation: %t %v", reserved, err)
			}
		})
	}
}

func TestSSHPortChangeRecoversUncertainStartup(t *testing.T) {
	st, m, rec, proxy := newAutoForwardFixture(t)
	rec.Network.SSHBind = "127.0.0.1"
	rec.Network.SSHPort = freeLoopbackPort(t)
	if err := st.SaveVM(rec); err != nil {
		t.Fatal(err)
	}
	unlock, err := st.TryLockVM(rec.ID)
	if err != nil || unlock == nil {
		t.Fatalf("VM lock: %v", err)
	}
	proxy.loseExposeResponse = true
	err = m.installDeclaredForwardsLocked(rec)
	unlock()
	if !errors.Is(err, gvproxy.ErrForwardOutcomeUnknown) {
		t.Fatal(err)
	}
	if err := process.StopRecorded(st.Runtime(rec).VMProcessRecord(), state.VMProcessKind(rec.Driver), processStopTimeout); err != nil {
		t.Fatal(err)
	}
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
			if err := process.StopRecorded(st.Runtime(rec).VMProcessRecord(), state.VMProcessKind(rec.Driver), processStopTimeout); err != nil {
				t.Fatal(err)
			}
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
