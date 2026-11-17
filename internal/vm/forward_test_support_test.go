package vm

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/gvproxy/forwarder"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

type forwardSocket struct {
	protocol, local string
}

type fakeAutoForwardGVProxy struct {
	mu                sync.Mutex
	ledger            http.Handler
	installed         map[forwardSocket]string
	exposes           int
	exposeStatus      int
	exposeStatuses    map[string]int
	unexposes         int
	unexposeStatus    int
	dropResponses     map[string]int
	waitResponses     map[string]bool
	finished          map[string]bool
	acknowledged      map[string]bool
	issued            uint64
	delayAction       string
	release           <-chan struct{}
	entered           chan<- string
	capabilityStatus  int
	capabilities      map[string]int
	acknowledgeStatus int
	acknowledgeDelay  time.Duration
	acknowledgements  int
	snapshotDelay     time.Duration
	snapshotOverride  *[]gvproxy.Forward
	snapshots         int
	submissions       int
}

func (f *fakeAutoForwardGVProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/services/gateway-forward/capabilities" {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.capabilityStatus != 0 {
			http.Error(w, "capability probe failed", f.capabilityStatus)
			return
		}
		capabilities := f.capabilities
		if capabilities == nil {
			capabilities = map[string]int{gvproxy.UDPForwardCapability: 1, gvproxy.ForwardRecoveryCapability: 1}
		}
		_ = json.NewEncoder(w).Encode(capabilities)
		return
	}
	r = r.Clone(r.Context())
	r.URL.Path = strings.TrimPrefix(r.URL.Path, "/services/forwarder")
	f.mu.Lock()
	if r.URL.Path == "/expose" || r.URL.Path == "/unexpose" {
		f.submissions++
	}
	ackStatus, ackDelay := f.acknowledgeStatus, f.acknowledgeDelay
	if r.URL.Path == "/acknowledge" {
		f.acknowledgements++
	}
	f.mu.Unlock()
	if r.URL.Path == "/acknowledge" {
		select {
		case <-time.After(ackDelay):
		case <-r.Context().Done():
			return
		}
		if ackStatus != 0 {
			http.Error(w, "acknowledgement failed", ackStatus)
			return
		}
	}
	response := httptest.NewRecorder()
	f.ledger.ServeHTTP(response, r)
	f.mu.Lock()
	if r.URL.Path == "/ids" && response.Code == 200 {
		var allocated struct {
			First uint64 `json:"first"`
		}
		_ = json.Unmarshal(response.Body.Bytes(), &allocated)
		count, _ := strconv.ParseUint(r.URL.Query().Get("count"), 10, 64)
		f.issued = allocated.First + count - 1
	}
	if r.URL.Path == "/acknowledge" && response.Code == 204 {
		if f.acknowledged == nil {
			f.acknowledged = make(map[string]bool)
		}
		f.acknowledged[r.URL.Query().Get("id")] = true
	}
	id := r.Header.Get("X-Voom-Forward-ID")
	if id != "" && response.Header().Get("X-Voom-Forward-Outcome") == "completed" {
		if f.finished == nil {
			f.finished = make(map[string]bool)
		}
		f.finished[id] = true
	}
	drop, wait := f.dropResponses[id], f.waitResponses[id]
	if drop > 0 {
		f.dropResponses[id]--
	}
	f.mu.Unlock()
	if wait {
		<-r.Context().Done()
		return
	}
	if drop != 0 {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return
	}
	for name, values := range response.Header() {
		w.Header()[name] = values
	}
	w.WriteHeader(response.Code)
	_, _ = w.Write(response.Body.Bytes())
}

func (f *fakeAutoForwardGVProxy) serveForwarder(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	if r.URL.Path == "/all" {
		f.snapshots++
		delay := f.snapshotDelay
		f.mu.Unlock()
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		var rows []gvproxy.Forward
		for socket, remote := range f.installed {
			rows = append(rows, gvproxy.Forward{Protocol: socket.protocol, Local: socket.local, Remote: remote})
		}
		if f.snapshotOverride != nil {
			rows = *f.snapshotOverride
		}
		_ = json.NewEncoder(w).Encode(rows)
		return
	}
	var endpoint gvproxy.Forward
	if err := json.NewDecoder(r.Body).Decode(&endpoint); err != nil {
		f.mu.Unlock()
		http.Error(w, "invalid endpoint", 400)
		return
	}
	action := strings.TrimPrefix(r.URL.Path, "/")
	status := http.StatusNoContent
	switch action {
	case "expose":
		f.exposes++
		if f.exposeStatus != 0 {
			status = f.exposeStatus
		}
		if specific, found := f.exposeStatuses[endpoint.Local]; found {
			status = specific
		}
	case "unexpose":
		f.unexposes++
		if f.unexposeStatus != 0 {
			status = f.unexposeStatus
		}
	default:
		f.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	release, entered := f.release, f.entered
	delayed := action == f.delayAction
	f.mu.Unlock()
	id := r.Header.Get("X-Voom-Forward-ID")
	if delayed {
		if entered != nil {
			entered <- id
		}
		<-release
	}
	f.mu.Lock()
	if status < 300 {
		if action == "expose" {
			f.installed[forwardSocket{endpoint.Protocol, endpoint.Local}] = endpoint.Remote
		} else {
			delete(f.installed, forwardSocket{endpoint.Protocol, endpoint.Local})
		}
	}
	f.mu.Unlock()
	w.WriteHeader(status)
	if status >= 300 {
		_, _ = w.Write([]byte("forwarding failed"))
	}
}

func (f *fakeAutoForwardGVProxy) dropNextResponses(count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dropResponses == nil {
		f.dropResponses = make(map[string]int)
	}
	f.dropResponses[strconv.FormatUint(f.issued+1, 10)] = count
}

func (f *fakeAutoForwardGVProxy) install(local string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, port, _ := net.SplitHostPort(local)
	f.installed[forwardSocket{"tcp", local}] = net.JoinHostPort(state.DefaultGuestTargetIP("qemu"), port)
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
	remote, ok := f.installed[forwardSocket{"tcp", local}]
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
			SSHPort:         freeLoopbackPort(t),
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
	writeProcessRecord(t, rt.GVProxyProcessRecord(), fakeNamedVMProcess(t, "gvproxy"))
	fake := &fakeAutoForwardGVProxy{installed: map[forwardSocket]string{{"tcp", forward.Address(vm.Network.SSHBind, vm.Network.SSHPort)}: forward.Address(state.DefaultGuestTargetIP(vm.Driver), 22)}}
	fake.ledger = forwarder.TrackMutationOutcomes(http.HandlerFunc(fake.serveForwarder))
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
	manager := New(st)
	manager.startSelfRecorded = func(args []string, _, recordPath string) error {
		watcher := fakeNamedVMProcess(t, "voom-test", args...)
		return process.Record(recordPath, watcher.Process.Pid)
	}
	return st, manager, vm, fake
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
