package vm

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

type fakeAutoForwardGVProxy struct {
	mu             sync.Mutex
	installed      map[string]string
	exposes        int
	unexposes      int
	unexposeStatus int
}

func (f *fakeAutoForwardGVProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var request map[string]string
	_ = json.NewDecoder(r.Body).Decode(&request)
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/services/forwarder/expose":
		f.exposes++
		f.installed[request["local"]] = request["remote"]
		w.WriteHeader(http.StatusNoContent)
	case "/services/forwarder/unexpose":
		f.unexposes++
		if f.unexposeStatus != 0 {
			http.Error(w, "unexpose failed", f.unexposeStatus)
			return
		}
		delete(f.installed, request["local"])
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeAutoForwardGVProxy) install(local string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installed[local] = ""
}

func (f *fakeAutoForwardGVProxy) failUnexpose(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unexposeStatus = status
}

func (f *fakeAutoForwardGVProxy) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exposes, f.unexposes
}

func (f *fakeAutoForwardGVProxy) remote(local string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	remote, ok := f.installed[local]
	return remote, ok
}

func newAutoForwardFixture(t *testing.T) (*state.Store, *Manager, *state.VMRecord, *fakeAutoForwardGVProxy) {
	t.Helper()
	root, err := os.MkdirTemp("", "vaf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("VOOM_STATE_DIR", filepath.Join(root, "s"))
	t.Setenv("VOOM_CONFIG_DIR", filepath.Join(root, "c"))
	t.Setenv("VOOM_CACHE_DIR", filepath.Join(root, "l"))
	t.Setenv("VOOM_RUNTIME_DIR", filepath.Join(root, "r"))
	st, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	im := &state.ImageRecord{
		SchemaVersion: state.SchemaVersion,
		ID:            "image-id",
		Name:          "image",
		Capabilities:  state.ImageCapabilities{ControlShare: true, GuestPortReport: true},
	}
	if err := os.MkdirAll(st.ImageDir(im.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(im.ID), "image.json"), im); err != nil {
		t.Fatal(err)
	}
	index := st.IndexSnapshot()
	index.Images[im.Name] = im.ID
	if err := st.SetIndexSnapshot(index); err != nil {
		t.Fatal(err)
	}
	vm := &state.VMRecord{
		SchemaVersion: state.SchemaVersion,
		ID:            "vm-id",
		Name:          "demo",
		Driver:        "qemu",
		CreatedAt:     now,
		UpdatedAt:     now,
		Image:         state.VMImageRef{ID: im.ID, Name: im.Name},
		Network: state.VMNetwork{
			AutoForward:     true,
			AutoForwardBind: "127.0.0.1",
			SSHBind:         "127.0.0.1",
		},
	}
	if err := os.MkdirAll(st.VMDir(vm.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVM(vm); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(vm.Name, vm.ID); err != nil {
		t.Fatal(err)
	}
	rt := st.Runtime(vm)
	if err := os.MkdirAll(st.ControlShareDir(vm), 0o755); err != nil {
		t.Fatal(err)
	}
	driver := fakeNamedVMProcess(t, "qemu-system-test")
	writeProcessRecord(t, rt.VMProcessRecord(), driver)
	fake := &fakeAutoForwardGVProxy{installed: map[string]string{}}
	listener, err := net.Listen("unix", rt.NetworkSock())
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: fake}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})
	return st, New(st), vm, fake
}

func writeAutoForwardReport(t *testing.T, st *state.Store, vm *state.VMRecord, report GuestPortsReport) {
	t.Helper()
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.ControlSharePortsPath(vm), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func TestReconcileKeepsRowsWithoutValidReport(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(*testing.T, *state.Store, *state.VMRecord)
	}{
		{name: "missing"},
		{name: "stale", write: func(t *testing.T, st *state.Store, vm *state.VMRecord) {
			writeAutoForwardReport(t, st, vm, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now().Add(-time.Minute)})
		}},
		{name: "malformed", write: func(t *testing.T, st *state.Store, vm *state.VMRecord) {
			if err := os.WriteFile(st.ControlSharePortsPath(vm), []byte("{"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, manager, vm, proxy := newAutoForwardFixture(t)
			row := RuntimeAutoForward{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 8080, GuestPort: 8080, GuestTargetIP: manager.GuestTargetIP(vm), Status: "active", Installed: true}
			proxy.install("127.0.0.1:8080")
			if err := manager.saveRuntimeAutoForwards(vm, []RuntimeAutoForward{row}); err != nil {
				t.Fatal(err)
			}
			if tc.write != nil {
				tc.write(t, st, vm)
			}
			if _, err := manager.ReconcileAutoForwards(context.Background(), vm.Name); err == nil {
				t.Fatal("expected report error")
			}
			rows, err := manager.ReadRuntimeAutoForwards(vm)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(rows, []RuntimeAutoForward{row}) {
				t.Fatalf("rows changed: %#v", rows)
			}
			if _, unexposes := proxy.counts(); unexposes != 0 {
				t.Fatalf("unexpose calls = %d, want 0", unexposes)
			}
		})
	}
}

func TestReconcileValidEmptyReportConfirmsRemovalBeforeEvent(t *testing.T) {
	st, manager, vm, proxy := newAutoForwardFixture(t)
	recorder := &recordingEmitter{}
	manager.events = recorder
	row := RuntimeAutoForward{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 8080, GuestPort: 8080, GuestTargetIP: manager.GuestTargetIP(vm), Status: "active", Installed: true}
	proxy.install("127.0.0.1:8080")
	if err := manager.saveRuntimeAutoForwards(vm, []RuntimeAutoForward{row}); err != nil {
		t.Fatal(err)
	}
	writeAutoForwardReport(t, st, vm, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now()})
	rows, err := manager.ReconcileAutoForwards(context.Background(), vm.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %#v", rows)
	}
	if _, unexposes := proxy.counts(); unexposes != 1 {
		t.Fatalf("unexpose calls = %d, want 1", unexposes)
	}
	if len(recorder.events) != 1 || recorder.events[0].Action != "uninstall" {
		t.Fatalf("events = %#v", recorder.events)
	}
}

func TestReconcileInstallsListenerFromValidReport(t *testing.T) {
	st, manager, vm, proxy := newAutoForwardFixture(t)
	recorder := &recordingEmitter{}
	manager.events = recorder
	port := freeLoopbackPort(t)
	writeAutoForwardReport(t, st, vm, GuestPortsReport{
		SchemaVersion: 1,
		GeneratedAt:   time.Now(),
		Listeners:     []GuestListener{{Proto: "tcp", Addr: "0.0.0.0", Port: port}},
	})
	rows, err := manager.ReconcileAutoForwards(context.Background(), vm.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].Installed || rows[0].HostPort != port {
		t.Fatalf("rows = %#v", rows)
	}
	if exposes, _ := proxy.counts(); exposes != 1 {
		t.Fatalf("expose calls = %d, want 1", exposes)
	}
	if len(recorder.events) != 1 || recorder.events[0].Action != "install" {
		t.Fatalf("events = %#v", recorder.events)
	}
}

func TestReconcileSkipsUnreadableRuntimeStateFromAnotherVM(t *testing.T) {
	st, manager, vm, _ := newAutoForwardFixture(t)
	port := freeLoopbackPort(t)
	other := *vm
	other.ID = "other-vm-id"
	other.Name = "other"
	if err := os.MkdirAll(st.VMDir(other.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVM(&other); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(other.Name, other.ID); err != nil {
		t.Fatal(err)
	}
	otherState := manager.RuntimeAutoForwardsPath(&other)
	if err := os.MkdirAll(filepath.Dir(otherState), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherState, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeAutoForwardReport(t, st, vm, GuestPortsReport{
		SchemaVersion: 1,
		GeneratedAt:   time.Now(),
		Listeners:     []GuestListener{{Proto: "tcp", Addr: "0.0.0.0", Port: port}},
	})
	rows, err := manager.ReconcileAutoForwards(context.Background(), vm.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].Installed || rows[0].HostPort != port {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestReconcileRemovesMismatchedRowOnce(t *testing.T) {
	st, manager, vm, proxy := newAutoForwardFixture(t)
	recorder := &recordingEmitter{}
	manager.events = recorder
	port := freeLoopbackPort(t)
	row := RuntimeAutoForward{Protocol: "tcp", Bind: "0.0.0.0", HostPort: port, GuestPort: port, GuestTargetIP: manager.GuestTargetIP(vm), Status: "active", Installed: true}
	proxy.install(net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
	if err := manager.saveRuntimeAutoForwards(vm, []RuntimeAutoForward{row}); err != nil {
		t.Fatal(err)
	}
	writeAutoForwardReport(t, st, vm, GuestPortsReport{
		SchemaVersion: 1,
		GeneratedAt:   time.Now(),
		Listeners:     []GuestListener{{Proto: "tcp", Addr: "0.0.0.0", Port: port}},
	})
	rows, err := manager.ReconcileAutoForwards(context.Background(), vm.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Bind != "127.0.0.1" || !rows[0].Installed {
		t.Fatalf("rows = %#v", rows)
	}
	exposes, unexposes := proxy.counts()
	if exposes != 1 || unexposes != 1 {
		t.Fatalf("expose calls = %d, unexpose calls = %d", exposes, unexposes)
	}
	if len(recorder.events) != 2 || recorder.events[0].Action != "uninstall" || recorder.events[1].Action != "install" {
		t.Fatalf("events = %#v", recorder.events)
	}
}

func TestReconcileDoesNotRepeatUnchangedSkipEvent(t *testing.T) {
	st, manager, vm, _ := newAutoForwardFixture(t)
	recorder := &recordingEmitter{}
	manager.events = recorder
	writeAutoForwardReport(t, st, vm, GuestPortsReport{
		SchemaVersion: 1,
		GeneratedAt:   time.Now(),
		Listeners:     []GuestListener{{Proto: "tcp", Addr: "0.0.0.0", Port: 22}},
	})
	if _, err := manager.ReconcileAutoForwards(context.Background(), vm.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ReconcileAutoForwards(context.Background(), vm.Name); err != nil {
		t.Fatal(err)
	}
	if len(recorder.events) != 1 || recorder.events[0].Action != "skip" {
		t.Fatalf("events = %#v", recorder.events)
	}
}

func TestReconcileReusesInstalledRowWhenGVProxyPIDIsUnknown(t *testing.T) {
	st, manager, vm, proxy := newAutoForwardFixture(t)
	port := freeLoopbackPort(t)
	row := RuntimeAutoForward{
		Protocol:      "tcp",
		Bind:          "127.0.0.1",
		HostPort:      port,
		GuestPort:     port,
		GuestTargetIP: manager.GuestTargetIP(vm),
		Status:        "active",
		Installed:     true,
	}
	proxy.install(net.JoinHostPort(row.Bind, strconv.Itoa(port)))
	if err := manager.saveRuntimeAutoForwards(vm, []RuntimeAutoForward{row}); err != nil {
		t.Fatal(err)
	}
	writeAutoForwardReport(t, st, vm, GuestPortsReport{
		SchemaVersion: 1,
		GeneratedAt:   time.Now(),
		Listeners:     []GuestListener{{Proto: "tcp", Addr: "0.0.0.0", Port: port}},
	})
	rows, err := manager.ReconcileAutoForwards(context.Background(), vm.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rows, []RuntimeAutoForward{row}) {
		t.Fatalf("rows = %#v", rows)
	}
	if exposes, _ := proxy.counts(); exposes != 0 {
		t.Fatalf("expose calls = %d, want 0", exposes)
	}
}

func TestReconcileRetainsRowWhenRemovalFails(t *testing.T) {
	st, manager, vm, proxy := newAutoForwardFixture(t)
	recorder := &recordingEmitter{}
	manager.events = recorder
	row := RuntimeAutoForward{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 8080, GuestPort: 8080, Status: "active", Installed: true}
	proxy.install("127.0.0.1:8080")
	proxy.failUnexpose(http.StatusServiceUnavailable)
	if err := manager.saveRuntimeAutoForwards(vm, []RuntimeAutoForward{row}); err != nil {
		t.Fatal(err)
	}
	writeAutoForwardReport(t, st, vm, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now()})
	rows, err := manager.ReconcileAutoForwards(context.Background(), vm.Name)
	if err == nil {
		t.Fatal("expected unexpose error")
	}
	if !reflect.DeepEqual(rows, []RuntimeAutoForward{row}) {
		t.Fatalf("rows = %#v", rows)
	}
	if len(recorder.events) != 0 {
		t.Fatalf("events = %#v", recorder.events)
	}
}

func TestUpdateAutoForwardRemovesMismatchedRows(t *testing.T) {
	enabled := true
	newBind := "127.0.0.1"
	newOffset := 2000
	for _, tc := range []struct {
		name      string
		oldBind   string
		oldOffset int
		update    AutoForwardUpdate
	}{
		{name: "bind", oldBind: "0.0.0.0", update: AutoForwardUpdate{Enabled: &enabled, Bind: &newBind}},
		{name: "offset", oldBind: "127.0.0.1", oldOffset: 1000, update: AutoForwardUpdate{Enabled: &enabled, Offset: &newOffset}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, manager, vm, proxy := newAutoForwardFixture(t)
			recorder := &recordingEmitter{}
			manager.events = recorder
			vm.Network.AutoForwardBind = tc.oldBind
			vm.Network.AutoForwardHostOffset = tc.oldOffset
			if err := manager.store.SaveVM(vm); err != nil {
				t.Fatal(err)
			}
			row := RuntimeAutoForward{Protocol: "tcp", Bind: tc.oldBind, HostPort: 8080 + tc.oldOffset, GuestPort: 8080, Offset: tc.oldOffset, Status: "active", Installed: true}
			proxy.install(net.JoinHostPort(row.Bind, strconv.Itoa(row.HostPort)))
			if err := manager.saveRuntimeAutoForwards(vm, []RuntimeAutoForward{row}); err != nil {
				t.Fatal(err)
			}
			watcher := fakeNamedVMProcess(t, "voom-test", "forward", "auto", "watch", vm.ID)
			writeProcessRecord(t, manager.store.Runtime(vm).AutoForwardProcessRecord(), watcher)
			updated, err := manager.UpdateAutoForward(context.Background(), vm.Name, tc.update)
			if err != nil {
				t.Fatal(err)
			}
			if tc.update.Bind != nil && updated.Network.AutoForwardBind != *tc.update.Bind {
				t.Fatalf("bind = %q", updated.Network.AutoForwardBind)
			}
			if tc.update.Offset != nil && updated.Network.AutoForwardHostOffset != *tc.update.Offset {
				t.Fatalf("offset = %d", updated.Network.AutoForwardHostOffset)
			}
			rows, err := manager.ReadRuntimeAutoForwards(vm)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 0 {
				t.Fatalf("rows = %#v", rows)
			}
			if _, unexposes := proxy.counts(); unexposes != 1 {
				t.Fatalf("unexpose calls = %d, want 1", unexposes)
			}
			if len(recorder.events) != 1 || recorder.events[0].Action != "uninstall" {
				t.Fatalf("events = %#v", recorder.events)
			}
		})
	}
}

func TestUpdateAutoForwardStartsWatcherWhenMismatchRemovalFails(t *testing.T) {
	_, manager, vm, proxy := newAutoForwardFixture(t)
	vm.Network.AutoForwardBind = "0.0.0.0"
	if err := manager.store.SaveVM(vm); err != nil {
		t.Fatal(err)
	}
	row := RuntimeAutoForward{Protocol: "tcp", Bind: "0.0.0.0", HostPort: 8080, GuestPort: 8080, Status: "active", Installed: true}
	proxy.install("0.0.0.0:8080")
	proxy.failUnexpose(http.StatusServiceUnavailable)
	if err := manager.saveRuntimeAutoForwards(vm, []RuntimeAutoForward{row}); err != nil {
		t.Fatal(err)
	}
	starts := 0
	manager.startSelfRecorded = func(args []string, _, recordPath string) error {
		starts++
		if !reflect.DeepEqual(args, []string{"forward", "auto", "watch", vm.ID}) {
			t.Fatalf("watcher arguments = %#v", args)
		}
		cmd := fakeNamedVMProcess(t, "voom-test", args...)
		if err := process.Record(recordPath, cmd.Process.Pid); err != nil {
			t.Fatal(err)
		}
		return nil
	}
	bind := "127.0.0.1"
	enabled := true
	updated, err := manager.UpdateAutoForward(context.Background(), vm.Name, AutoForwardUpdate{Enabled: &enabled, Bind: &bind})
	if err == nil {
		t.Fatal("expected mismatch removal error")
	}
	if starts != 1 {
		t.Fatalf("watcher starts = %d, want 1", starts)
	}
	if updated == nil || updated.Network.AutoForwardBind != "127.0.0.1" {
		t.Fatalf("updated VM = %#v", updated)
	}
	rows, readErr := manager.ReadRuntimeAutoForwards(vm)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !reflect.DeepEqual(rows, []RuntimeAutoForward{row}) {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestDisableAutoForwardRetainsRowsWhenRemovalFails(t *testing.T) {
	_, manager, vm, proxy := newAutoForwardFixture(t)
	row := RuntimeAutoForward{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 8080, GuestPort: 8080, Status: "active", Installed: true}
	proxy.install("127.0.0.1:8080")
	proxy.failUnexpose(http.StatusServiceUnavailable)
	if err := manager.saveRuntimeAutoForwards(vm, []RuntimeAutoForward{row}); err != nil {
		t.Fatal(err)
	}
	watcher := fakeNamedVMProcess(t, "voom-test", "forward", "auto", "watch", vm.ID)
	writeProcessRecord(t, manager.store.Runtime(vm).AutoForwardProcessRecord(), watcher)
	enabled := false
	updated, err := manager.UpdateAutoForward(context.Background(), vm.Name, AutoForwardUpdate{Enabled: &enabled})
	if err == nil {
		t.Fatal("expected removal error")
	}
	if updated == nil || updated.Network.AutoForward {
		t.Fatalf("updated VM = %#v", updated)
	}
	rows, readErr := manager.ReadRuntimeAutoForwards(vm)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !reflect.DeepEqual(rows, []RuntimeAutoForward{row}) {
		t.Fatalf("rows = %#v", rows)
	}
	if !process.Alive(watcher.Process.Pid) {
		t.Fatal("watcher stopped after cleanup failure")
	}
	proxy.failUnexpose(0)
	outcome, retryErr := manager.watchAutoForwardsOnce(vm.ID)
	if retryErr != nil || outcome != autoForwardWatchDone {
		t.Fatalf("retry outcome = %d, error = %v", outcome, retryErr)
	}
	rows, readErr = manager.ReadRuntimeAutoForwards(vm)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(rows) != 0 {
		t.Fatalf("rows after retry = %#v", rows)
	}
}

func TestUpdateAutoForwardOffsetPreservesDisabledState(t *testing.T) {
	_, manager, vm, _ := newAutoForwardFixture(t)
	vm.Network.AutoForward = false
	if err := manager.store.SaveVM(vm); err != nil {
		t.Fatal(err)
	}
	offset := 1000
	updated, err := manager.UpdateAutoForward(context.Background(), vm.Name, AutoForwardUpdate{Offset: &offset})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Network.AutoForward || updated.Network.AutoForwardHostOffset != 1000 {
		t.Fatalf("network = %#v", updated.Network)
	}
}

func TestWatcherSkipsBusyVMAndReloadsByID(t *testing.T) {
	_, manager, vm, proxy := newAutoForwardFixture(t)
	vm.Network.AutoForwardBind = "0.0.0.0"
	if err := manager.store.SaveVM(vm); err != nil {
		t.Fatal(err)
	}
	row := RuntimeAutoForward{Protocol: "tcp", Bind: "0.0.0.0", HostPort: 8080, GuestPort: 8080, Status: "active", Installed: true}
	proxy.install("0.0.0.0:8080")
	if err := manager.saveRuntimeAutoForwards(vm, []RuntimeAutoForward{row}); err != nil {
		t.Fatal(err)
	}
	unlock, err := manager.store.TryLockVM(vm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unlock == nil {
		t.Fatal("VM lock was unavailable")
	}
	vm.Network.AutoForwardBind = "127.0.0.1"
	if err := manager.store.SaveVM(vm); err != nil {
		unlock()
		t.Fatal(err)
	}
	outcome, err := manager.watchAutoForwardsOnce(vm.ID)
	if err != nil || outcome != autoForwardWatchSkipped {
		unlock()
		t.Fatalf("busy iteration = %d, %v", outcome, err)
	}
	unlock()
	outcome, err = manager.watchAutoForwardsOnce(vm.ID)
	if err == nil || outcome != autoForwardWatchContinue {
		t.Fatalf("next iteration = %d, %v", outcome, err)
	}
	rows, readErr := manager.ReadRuntimeAutoForwards(vm)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %#v", rows)
	}
	if _, unexposes := proxy.counts(); unexposes != 1 {
		t.Fatalf("unexpose calls = %d, want 1", unexposes)
	}
}

func TestWatcherDeduplicatesErrorsAndLogsRecovery(t *testing.T) {
	st, manager, vm, _ := newAutoForwardFixture(t)
	lastErr := ""
	first := errors.New("first failure")
	second := errors.New("second failure")
	lastErr = manager.logAutoForwardWatchResult(vm, autoForwardWatchContinue, first, lastErr)
	lastErr = manager.logAutoForwardWatchResult(vm, autoForwardWatchContinue, first, lastErr)
	lastErr = manager.logAutoForwardWatchResult(vm, autoForwardWatchSkipped, second, lastErr)
	lastErr = manager.logAutoForwardWatchResult(vm, autoForwardWatchContinue, second, lastErr)
	lastErr = manager.logAutoForwardWatchResult(vm, autoForwardWatchContinue, nil, lastErr)
	lastErr = manager.logAutoForwardWatchResult(vm, autoForwardWatchContinue, nil, lastErr)

	logPath := st.LogPath(vm, "auto-forward")
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"error: first failure", "error: second failure", "info: reconciliation recovered"} {
		if count := strings.Count(string(b), msg); count != 1 {
			t.Fatalf("%q log count = %d, want 1; log = %q", msg, count, b)
		}
	}
	if lastErr != "" {
		t.Fatalf("last error = %q, want empty", lastErr)
	}
}

func TestReconcileLockWaitHonorsContext(t *testing.T) {
	_, manager, vm, _ := newAutoForwardFixture(t)
	unlock, err := manager.store.TryLockVM(vm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unlock == nil {
		t.Fatal("VM lock was unavailable")
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := manager.ReconcileAutoForwards(ctx, vm.Name); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ReconcileAutoForwards error = %v, want context deadline", err)
	}
}

func TestReconcileRejectsStoppedVM(t *testing.T) {
	st, manager, vm, _ := newAutoForwardFixture(t)
	recordPath := st.Runtime(vm).VMProcessRecord()
	if err := os.Remove(recordPath); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ReconcileAutoForwards(context.Background(), vm.Name); err == nil || !strings.Contains(err.Error(), "is not running") {
		t.Fatalf("ReconcileAutoForwards error = %v", err)
	}
}

func TestReconcileRollsBackRemovalAfterStateSaveFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not block root")
	}
	st, manager, vm, proxy := newAutoForwardFixture(t)
	recorder := &recordingEmitter{}
	manager.events = recorder
	port := freeLoopbackPort(t)
	local := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	row := RuntimeAutoForward{Protocol: "tcp", Bind: "127.0.0.1", HostPort: port, GuestPort: port, GuestTargetIP: manager.GuestTargetIP(vm), Status: "active", Installed: true}
	proxy.install(local)
	if err := manager.saveRuntimeAutoForwards(vm, []RuntimeAutoForward{row}); err != nil {
		t.Fatal(err)
	}
	writeAutoForwardReport(t, st, vm, GuestPortsReport{SchemaVersion: 1, GeneratedAt: time.Now()})
	runtimeDir := st.Runtime(vm).Dir()
	t.Cleanup(func() { _ = os.Chmod(runtimeDir, 0o755) })
	if err := os.Chmod(runtimeDir, 0o555); err != nil {
		t.Fatal(err)
	}
	_, firstErr := manager.ReconcileAutoForwards(context.Background(), vm.Name)
	if err := os.Chmod(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if firstErr == nil {
		t.Fatal("expected state save failure")
	}
	rows, err := manager.ReadRuntimeAutoForwards(vm)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rows, []RuntimeAutoForward{row}) || len(recorder.events) != 0 {
		t.Fatalf("rows = %#v, events = %#v", rows, recorder.events)
	}
	wantRemote := net.JoinHostPort(row.GuestTargetIP, strconv.Itoa(row.GuestPort))
	if remote, ok := proxy.remote(local); !ok || remote != wantRemote {
		t.Fatalf("restored remote = %q, present = %t; want %q", remote, ok, wantRemote)
	}
	exposes, unexposes := proxy.counts()
	if exposes != 1 || unexposes != 1 {
		t.Fatalf("expose calls = %d, unexpose calls = %d", exposes, unexposes)
	}
	writeAutoForwardReport(t, st, vm, GuestPortsReport{
		SchemaVersion: 1,
		GeneratedAt:   time.Now(),
		Listeners:     []GuestListener{{Proto: "tcp", Addr: "0.0.0.0", Port: port}},
	})
	rows, err = manager.ReconcileAutoForwards(context.Background(), vm.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rows, []RuntimeAutoForward{row}) || len(recorder.events) != 0 {
		t.Fatalf("rows = %#v, events = %#v", rows, recorder.events)
	}
	if exposes, unexposes := proxy.counts(); exposes != 1 || unexposes != 1 {
		t.Fatalf("expose calls = %d, unexpose calls = %d", exposes, unexposes)
	}
}

func TestReconcileRollsBackExposureAfterStateSaveFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not block root")
	}
	st, manager, vm, proxy := newAutoForwardFixture(t)
	port := freeLoopbackPort(t)
	writeAutoForwardReport(t, st, vm, GuestPortsReport{
		SchemaVersion: 1,
		GeneratedAt:   time.Now(),
		Listeners:     []GuestListener{{Proto: "tcp", Addr: "0.0.0.0", Port: port}},
	})
	runtimeDir := st.Runtime(vm).Dir()
	t.Cleanup(func() { _ = os.Chmod(runtimeDir, 0o755) })
	if err := os.Chmod(runtimeDir, 0o555); err != nil {
		t.Fatal(err)
	}
	_, reconcileErr := manager.ReconcileAutoForwards(context.Background(), vm.Name)
	if err := os.Chmod(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if reconcileErr == nil {
		t.Fatal("expected state save failure")
	}
	exposes, unexposes := proxy.counts()
	if exposes != 1 || unexposes != 1 {
		t.Fatalf("expose calls = %d, unexpose calls = %d", exposes, unexposes)
	}
	rows, err := manager.ReadRuntimeAutoForwards(vm)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %#v", rows)
	}
}
