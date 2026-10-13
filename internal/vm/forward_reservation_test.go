package vm

import (
	"context"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/state"
)

func TestAutoForwardJournalPrecedesExpose(t *testing.T) {
	st, _ := newTestStore(t)
	rec := registerForwardReservationVM(t, st)
	m := New(st)
	rt := st.Runtime(rec)
	if err := os.MkdirAll(rt.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	port := freeLoopbackPort(t)
	endpoint := forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: 8080}
	listener, err := net.Listen("unix", rt.NetworkSock())
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/services/forwarder/expose" {
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
		journal, err := forward.ReadMutationReservations(rt.ForwardMutation())
		if err != nil || len(journal) != 1 || journal[0] != endpoint {
			t.Errorf("reservation before expose: %#v %v", journal, err)
		}
		rows, err := m.ReadRuntimeAutoForwards(rec)
		if err != nil || len(rows) != 0 {
			t.Errorf("unconfirmed results published before expose: %#v %v", rows, err)
		}
		if err := st.WithGlobal(func() error {
			reserved, err := st.PortReservations(rec.ID)
			if err == nil && !reserved.Reserved(endpoint.Protocol, "0.0.0.0", port) {
				t.Error("competing allocator cannot see proposed reservation")
			}
			return err
		}); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusNoContent)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	unlock, err := st.TryLockVM(rec.ID)
	if err != nil || unlock == nil {
		t.Fatalf("VM lock: %v", err)
	}
	defer unlock()
	plans := []forward.AutoPlan{{Protocol: endpoint.Protocol, Bind: endpoint.Bind, HostPort: port, GuestPort: endpoint.GuestPort, GuestTargetIP: m.GuestTargetIP(rec)}}
	rows, err := m.installAutoForwardRowsLocked(rec, plans, nil)
	if err != nil || len(rows) != 1 || !rows[0].Installed() {
		t.Fatalf("confirmed result: %#v %v", rows, err)
	}
	if m.forwardMutationPending(rec) {
		t.Fatal("confirmed result retained its journal")
	}
}

func TestAutoForwardRejectsReservationClaimedAfterPlanning(t *testing.T) {
	st, manager, rec, proxy := newAutoForwardFixture(t)
	port := freeLoopbackPort(t)
	report := GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now(), Listeners: []GuestListener{{Proto: "tcp", Addr: "0.0.0.0", Port: port}}}
	plans, err := manager.PlanAutoForwards(rec, report, nil)
	if err != nil || len(plans) != 1 || plans[0].Reason != "" {
		t.Fatalf("initial plan: %#v %v", plans, err)
	}
	otherStore, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	other := registerForwardReservationVM(t, otherStore)
	if _, err := New(otherStore).AddForward(context.Background(), other.Name, 8080, port, "127.0.0.1", false, "tcp"); err != nil {
		t.Fatal(err)
	}
	unlock, err := st.TryLockVM(rec.ID)
	if err != nil || unlock == nil {
		t.Fatalf("VM lock: %v", err)
	}
	defer unlock()
	rows, err := manager.installAutoForwardRowsLocked(rec, plans, nil)
	if err != nil || len(rows) != 1 || rows[0].Status != forward.ReservationSkipped || rows[0].Reason != "host port is reserved by SSH or another forward" {
		t.Fatalf("conflicting reservation: %#v %v", rows, err)
	}
	persisted, err := manager.ReadRuntimeAutoForwards(rec)
	if err != nil || len(persisted) != 1 || persisted[0].Installed() {
		t.Fatalf("persisted conflict: %#v %v", persisted, err)
	}
	if exposes, _ := proxy.counts(); exposes != 0 {
		t.Fatalf("conflicting port received %d expose requests", exposes)
	}
}

func TestAutoForwardWatcherSeesVMsCreatedByAnotherStore(t *testing.T) {
	_, manager, rec, proxy := newAutoForwardFixture(t)
	port := freeLoopbackPort(t)
	otherStore, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	other := registerForwardReservationVM(t, otherStore)
	if _, err := New(otherStore).AddForward(context.Background(), other.Name, 8080, port, "127.0.0.1", false, "tcp"); err != nil {
		t.Fatal(err)
	}
	writeAutoForwardReport(t, otherStore, rec, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now(), Listeners: []GuestListener{{Proto: "tcp", Addr: "0.0.0.0", Port: port}}})
	outcome, err := manager.watchAutoForwardsOnce(rec.ID)
	if err != nil || outcome != autoForwardWatchContinue {
		t.Fatalf("watch iteration: %d %v", outcome, err)
	}
	rows, err := manager.ReadRuntimeAutoForwards(rec)
	if err != nil || len(rows) != 1 || rows[0].Status != forward.ReservationSkipped {
		t.Fatalf("watcher reservation: %#v %v", rows, err)
	}
	if exposes, _ := proxy.counts(); exposes != 0 {
		t.Fatalf("reserved port received %d expose requests", exposes)
	}
}

func registerForwardReservationVM(t *testing.T, store *state.Store) *state.VMRecord {
	t.Helper()
	rec := &state.VMRecord{SchemaVersion: state.SchemaVersion, ID: "other", Name: "other", Driver: "qemu", Network: state.VMNetwork{SSHBind: "127.0.0.1"}}
	if err := os.MkdirAll(store.VMDir(rec.ID), 0755); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveVM(rec); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterVM(rec.Name, rec.ID); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestForwardRowsDerivesUncertaintyWithoutChangingReservations(t *testing.T) {
	for _, status := range []forward.ReservationState{forward.ReservationActive, forward.ReservationSkipped} {
		t.Run(string(status), func(t *testing.T) {
			st, manager, rec, _ := newAutoForwardFixture(t)
			row := RuntimeAutoForward{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 8080, GuestPort: 8080, Status: status}
			if err := manager.saveRuntimeAutoForwards(rec, []RuntimeAutoForward{row}); err != nil {
				t.Fatal(err)
			}
			if err := state.WriteJSONAtomic(st.Runtime(rec).ForwardMutation(), []forward.Decl{row.Endpoint()}); err != nil {
				t.Fatal(err)
			}
			rows, err := manager.ForwardRows(context.Background(), []string{rec.Name})
			if err != nil || len(rows) != 2 {
				t.Fatalf("display rows: %#v %v", rows, err)
			}
			wantStatus := "uncertain"
			if status == forward.ReservationSkipped {
				wantStatus = "skipped"
			}
			if rows[1].Status != wantStatus || rows[1].Installed {
				t.Fatalf("display state: %#v", rows[1])
			}
			persisted, err := manager.ReadRuntimeAutoForwards(rec)
			if err != nil || len(persisted) != 1 || persisted[0] != row {
				t.Fatalf("last confirmed state changed: %#v %v", persisted, err)
			}
		})
	}
}

func TestAutoForwardResultsFollowEndpointIdentity(t *testing.T) {
	st, m, rec, proxy := newAutoForwardFixture(t)
	ports := make(map[int]struct{})
	newPlan := func() forward.AutoPlan {
		for {
			port := freeLoopbackPort(t)
			if _, used := ports[port]; used {
				continue
			}
			ports[port] = struct{}{}
			return forward.AutoPlan{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: port, GuestTargetIP: m.GuestTargetIP(rec)}
		}
	}
	first, conflict, retained, rejected, last, skipped := newPlan(), newPlan(), newPlan(), newPlan(), newPlan(), newPlan()
	skipped.Reason = "listener is not bound to all guest interfaces"
	other := registerForwardReservationVM(t, st)
	if _, err := New(st).AddForward(context.Background(), other.Name, conflict.GuestPort, conflict.HostPort, conflict.Bind, false, "tcp"); err != nil {
		t.Fatal(err)
	}
	old := RuntimeAutoForward{
		Protocol: retained.Protocol, Bind: retained.Bind, HostPort: retained.HostPort, GuestPort: retained.GuestPort,
		GuestTargetIP: retained.GuestTargetIP, Status: forward.ReservationActive,
	}
	if err := m.saveRuntimeAutoForwards(rec, []RuntimeAutoForward{old}); err != nil {
		t.Fatal(err)
	}
	proxy.install(forward.Address(old.Bind, old.HostPort))
	proxy.exposeStatuses = map[string]int{forward.Address(rejected.Bind, rejected.HostPort): 409}
	unlock, err := st.TryLockVM(rec.ID)
	if err != nil || unlock == nil {
		t.Fatalf("VM lock: %v", err)
	}
	defer unlock()
	rows, err := m.installAutoForwardRowsLocked(rec, []forward.AutoPlan{first, conflict, retained, rejected, last, skipped}, []RuntimeAutoForward{old})
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := m.ReadRuntimeAutoForwards(rec)
	if err != nil || len(persisted) != len(rows) || len(rows) != 6 {
		t.Fatalf("confirmed results: %#v %v", persisted, err)
	}
	want := map[int]forward.ReservationState{
		first.HostPort: forward.ReservationActive, conflict.HostPort: forward.ReservationSkipped,
		retained.HostPort: forward.ReservationActive, rejected.HostPort: forward.ReservationSkipped,
		last.HostPort: forward.ReservationActive, skipped.HostPort: forward.ReservationSkipped,
	}
	for _, row := range persisted {
		if status, exists := want[row.HostPort]; !exists || row.Status != status {
			t.Fatalf("result assigned to wrong endpoint: %#v", row)
		}
	}
	if exposes, unexposes := proxy.counts(); exposes != 3 || unexposes != 0 {
		t.Fatalf("backend requests: expose=%d unexpose=%d", exposes, unexposes)
	}
	if m.forwardMutationPending(rec) {
		t.Fatal("confirmed results retained their journal")
	}
}
