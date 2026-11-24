package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/doctor"
	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

func TestForwardingWarningsAcrossInspectionCommands(t *testing.T) {
	root, err := os.MkdirTemp("", "voom-warnings-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	for _, kind := range []string{"CONFIG", "STATE", "CACHE", "RUNTIME"} {
		t.Setenv("VOOM_"+kind+"_DIR", filepath.Join(root, kind))
	}
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 18080, GuestPort: 8080}
	record := &state.VMRecord{SchemaVersion: state.SchemaVersion, ID: "vm1", Name: "dev", Driver: "qemu", Network: state.VMNetwork{SSHBind: "127.0.0.1", SSHPort: 2222, Forwards: []forward.Decl{endpoint}}}
	if err := store.SaveVM(record); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterVM(record.Name, record.ID); err != nil {
		t.Fatal(err)
	}
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "qemu-system-test")
	if err := state.CopyFile(context.Background(), shell, executable); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(executable, 0700); err != nil {
		t.Fatal(err)
	}
	driver := exec.Command(executable, "-c", "while true; do sleep 1; done")
	if err := driver.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Process.Kill(); _ = driver.Wait() })
	rt := store.Runtime(record)
	if err := os.MkdirAll(rt.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", rt.NetworkSock())
	if err != nil {
		t.Fatal(err)
	}
	var table atomic.Value
	var removeDriverRecord atomic.Bool
	var mutations atomic.Int32
	var active, peak atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutations.Add(1)
			http.Error(w, "inspection must not mutate forwarding", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/services/forwarder/all" {
			http.NotFound(w, r)
			return
		}
		body := table.Load().(string)
		if removeDriverRecord.Swap(false) {
			if err := os.Remove(rt.VMProcessRecord()); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if body == "blocked" {
			current := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); current > old; old = peak.Load() {
				if peak.CompareAndSwap(old, current) {
					break
				}
			}
			<-r.Context().Done()
			return
		}
		if body == "" {
			http.Error(w, "snapshot unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprint(w, body)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	target := state.DefaultGuestTargetIP(record.Driver)
	sshOnly := fmt.Sprintf(`[{"protocol":"tcp","local":"127.0.0.1:2222","remote":%q}]`, forward.Address(target, 22))
	all := strings.TrimSuffix(sshOnly, "]") + fmt.Sprintf(`,{"protocol":"tcp","local":"127.0.0.1:18080","remote":%q}]`, forward.Address(target, 8080))
	journal, err := json.Marshal(forward.MutationJournal{Version: 1, Process: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Requests: []forward.MutationRequest{{ID: "request-1", Kind: forward.ManualExpose, Endpoint: endpoint, GuestTargetIP: target, Result: forward.UnknownMutation(), Error: "response lost"}}})
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := json.Marshal([]forward.Decl{endpoint})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, status, table, journal, warning string
		autoRows                              []forward.RuntimeAuto
	}{
		{name: "stopped"},
		{name: "installed", status: "running", table: all},
		{name: "driver socket without record", status: "unknown", warning: "remains without process identity"},
		{name: "skipped automatic forward", status: "running", table: all, autoRows: []forward.RuntimeAuto{{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 10022, GuestPort: 22, GuestTargetIP: target, Status: forward.ReservationSkipped, Reason: "guest SSH port is reserved"}}},
		{name: "missing manual forward", status: "running", table: sshOnly, warning: "tcp 127.0.0.1:18080: declared forward is absent"},
		{name: "missing automatic forward", status: "running", table: all, autoRows: []forward.RuntimeAuto{{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 19090, GuestPort: 9090, GuestTargetIP: target, Status: forward.ReservationActive}}, warning: "tcp 127.0.0.1:19090: declared forward is absent"},
		{name: "pending recovery", status: "running", journal: string(journal), warning: "voom forward reconcile dev"},
		{name: "stopped with pending recovery", journal: string(journal), warning: "voom forward reconcile dev"},
		{name: "legacy recovery", status: "running", journal: string(legacy), warning: "recovery details are unavailable"},
		{name: "malformed journal", status: "running", journal: "{", warning: "malformed"},
		{name: "unavailable observation", status: "running", warning: "snapshot unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := tc.status
			if status == "" {
				status = "stopped"
			}
			wantRunning := status == "running"
			wantUnknown := status == "unknown"
			table.Store(tc.table)
			if len(tc.autoRows) > 0 {
				if err := forward.WriteRuntimeState(rt.AutoForwardsJSON(), tc.autoRows); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(rt.AutoForwardsJSON()); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if wantRunning {
				if err := process.Record(rt.VMProcessRecord(), driver.Process.Pid); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(rt.VMProcessRecord()); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if tc.journal != "" {
				if err := os.WriteFile(rt.ForwardMutation(), []byte(tc.journal), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(rt.ForwardMutation()); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			wantWarning := tc.warning != ""
			if wantUnknown {
				socket, err := net.Listen("unix", rt.QEMUMonitor())
				if err != nil {
					t.Fatal(err)
				}
				defer socket.Close()
			}
			list := runCmd(t, "list")
			if strings.Contains(list, "forwarding=warning") != wantWarning || !strings.Contains(list, status) {
				t.Fatalf("list: %s", list)
			}
			var rows []struct {
				Status             string   `json:"status"`
				ForwardingWarnings []string `json:"forwardingWarnings"`
			}
			if err := json.Unmarshal([]byte(runCmd(t, "list", "--output", "json")), &rows); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].Status != status || (len(rows[0].ForwardingWarnings) > 0) != wantWarning {
				t.Fatalf("list JSON: %+v", rows)
			}
			info := runCmd(t, "info", record.Name)
			if strings.Contains(info, "forwarding-warning:") != wantWarning || !strings.Contains(info, "status: "+status) || !strings.Contains(info, tc.warning) || strings.Contains(info, "runtime-error: cannot confirm runtime state") != wantUnknown {
				t.Fatalf("info: %s", info)
			}
			var details struct {
				Status             string   `json:"status"`
				RuntimeError       string   `json:"runtimeError"`
				Running            bool     `json:"running"`
				ForwardingWarnings []string `json:"forwardingWarnings"`
			}
			if err := json.Unmarshal([]byte(runCmd(t, "info", record.Name, "--output", "json")), &details); err != nil {
				t.Fatal(err)
			}
			if details.Running != wantRunning || (len(details.ForwardingWarnings) > 0) != wantWarning || !strings.Contains(strings.Join(details.ForwardingWarnings, "\n"), tc.warning) {
				t.Fatalf("info JSON: %+v", details)
			}
			if details.Status != status || (details.RuntimeError != "") != wantUnknown || (wantUnknown && !strings.Contains(details.RuntimeError, tc.warning)) {
				t.Fatalf("info JSON runtime: %+v", details)
			}
			found := false
			for _, check := range doctor.StateDiagnostics(store) {
				if check.Name == "forwarding-dev" {
					found = true
					if check.OK || check.Severity != "warn" || !strings.Contains(check.Message, tc.warning) {
						t.Fatalf("doctor: %+v", check)
					}
				}
			}
			if found != wantWarning {
				t.Fatalf("doctor warning present = %t, want %t", found, wantWarning)
			}
			if mutations.Load() != 0 {
				t.Fatal("inspection attempted a forwarding mutation")
			}
			if tc.journal != "" {
				data, err := os.ReadFile(rt.ForwardMutation())
				if err != nil || string(data) != tc.journal {
					t.Fatalf("inspection changed recovery journal: %s, %v", data, err)
				}
			}
			if _, running := process.ValidRecord(rt.VMProcessRecord(), "qemu"); running != wantRunning {
				t.Fatal("inspection changed driver state")
			}
		})
	}
	t.Run("status and forwarding use the same runtime observation", func(t *testing.T) {
		t.Cleanup(func() {
			if err := process.Record(rt.VMProcessRecord(), driver.Process.Pid); err != nil {
				t.Error(err)
			}
		})
		for _, command := range []string{"list", "info"} {
			if err := process.Record(rt.VMProcessRecord(), driver.Process.Pid); err != nil {
				t.Fatal(err)
			}
			table.Store(all)
			removeDriverRecord.Store(true)
			args := []string{command, "--output", "json"}
			if command == "info" {
				args = append(args, record.Name)
			}
			output := runCmd(t, args...)
			if !strings.Contains(output, `"status":"running"`) || strings.Contains(output, `"forwardingWarnings"`) {
				t.Errorf("%s combined different runtime observations: %s", command, output)
			}
			if _, err := os.Stat(rt.VMProcessRecord()); !os.IsNotExist(err) {
				t.Fatalf("forwarding probe did not remove the process record: %v", err)
			}
		}
	})
	t.Run("listing deadline and order", func(t *testing.T) {
		table.Store("blocked")
		for i := range 11 {
			other := *record
			other.Name, other.ID = fmt.Sprintf("dev-%02d", i), fmt.Sprintf("vm-%02d", i)
			if err := store.SaveVM(&other); err != nil {
				t.Fatal(err)
			}
			if err := store.RegisterVM(other.Name, other.ID); err != nil {
				t.Fatal(err)
			}
			otherRuntime := store.Runtime(&other)
			if err := process.Record(otherRuntime.VMProcessRecord(), driver.Process.Pid); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(rt.NetworkSock(), otherRuntime.NetworkSock()); err != nil {
				t.Fatal(err)
			}
		}
		start := time.Now()
		output := runCmd(t, "list", "--output", "json")
		t.Logf("list with twelve blocked VMs: %s", time.Since(start))
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("list probed VMs serially: %s", elapsed)
		}
		if peak.Load() > 8 || peak.Load() < 2 {
			t.Fatalf("list probe concurrency: %d", peak.Load())
		}
		if err := process.WaitFor(func() bool { return active.Load() == 0 }, time.Second); err != nil {
			t.Fatal(err)
		}
		peak.Store(0)
		var rows []struct {
			Name     string   `json:"name"`
			Status   string   `json:"status"`
			Warnings []string `json:"forwardingWarnings"`
		}
		if err := json.Unmarshal([]byte(output), &rows); err != nil {
			t.Fatal(err)
		}
		start = time.Now()
		var names []string
		for _, check := range doctor.StateDiagnostics(store) {
			if strings.HasPrefix(check.Name, "forwarding-") {
				names = append(names, strings.TrimPrefix(check.Name, "forwarding-"))
			}
		}
		t.Logf("doctor forwarding checks with twelve blocked VMs: %s", time.Since(start))
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("doctor probed VMs serially: %s", elapsed)
		}
		if len(rows) != 12 || len(names) != len(rows) || peak.Load() > 8 || peak.Load() < 2 {
			t.Fatalf("probe batch: rows=%d, doctor=%v, concurrent=%d", len(rows), names, peak.Load())
		}
		for i, row := range rows {
			if row.Name != names[i] || row.Status != "running" || len(row.Warnings) == 0 || (i > 0 && row.Name <= rows[i-1].Name) {
				t.Fatalf("inspection order, runtime status, or warnings: %+v, doctor=%v", rows, names)
			}
		}
	})
}
