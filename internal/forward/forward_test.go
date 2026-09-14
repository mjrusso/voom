package forward

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeBindAndConflictRules(t *testing.T) {
	if _, err := NormalizeBind("localhost"); err == nil {
		t.Fatal("expected localhost bind rejection")
	}
	if got, err := NormalizeBind("127.0.0.1"); err != nil || got != "127.0.0.1" {
		t.Fatalf("NormalizeBind = %q, %v", got, err)
	}
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.0.0.0", "127.0.0.1", true},
		{"192.0.2.10", "0.0.0.0", true},
		{"::", "127.0.0.1", true},
		{"::", "::1", true},
		{"::1", "::1", true},
		{"::1", "2001:db8::1", false},
		{"::1", "0.0.0.0", false},
		{"127.0.0.1", "192.0.2.10", false},
	}
	for _, tc := range cases {
		if got := BindsConflict(tc.a, tc.b); got != tc.want {
			t.Fatalf("BindsConflict(%q, %q) = %t, want %t", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestPlanAutoSelectionOffsetAndConflicts(t *testing.T) {
	report := PortsReport{SchemaVersion: 1, GeneratedAt: time.Now().UTC(), Listeners: []Listener{
		{Proto: "tcp", Addr: "0.0.0.0", Port: 8080},
		{Proto: "tcp", Addr: "127.0.0.1", Port: 9090},
		{Proto: "tcp", Addr: "0.0.0.0", Port: 22},
		{Proto: "tcp", Addr: "0.0.0.0", Port: 18080},
		{Proto: "tcp", Addr: "0.0.0.0", Port: 70000},
	}}
	rows := mustPlanAuto(t, VMPlanConfig{HostOffset: 10000, GuestTargetIP: "192.168.127.3"}, report, nil, PlanOptions{
		PortReserved: func(_ string, port int) (bool, error) { return port == 28080, nil },
		HostPortAvailable: func(_ string, port int) (bool, string) {
			if port == 19090 {
				return false, "host port is already in use"
			}
			return true, ""
		},
	})
	assertRow(t, rows[0], "active", 18080, "")
	assertRow(t, rows[1], "skipped", 19090, "interfaces")
	assertRow(t, rows[2], "skipped", 10022, "SSH")
	assertRow(t, rows[3], "skipped", 28080, "reserved")
	assertRow(t, rows[4], "skipped", 80000, "outside")
}

func TestPlanAutoKeepsExistingInstalledPortWithoutAvailabilityCheck(t *testing.T) {
	report := PortsReport{SchemaVersion: 1, GeneratedAt: time.Now().UTC(), Listeners: []Listener{{Proto: "tcp", Addr: "0.0.0.0", Port: 8080}}}
	existing := []RuntimeAuto{{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 8080, GuestPort: 8080, Installed: true}}
	rows := mustPlanAuto(t, VMPlanConfig{GuestTargetIP: "192.168.127.3"}, report, existing, PlanOptions{
		HostPortAvailable: func(string, int) (bool, string) { return false, "host port is already in use" },
	})
	assertRow(t, rows[0], "active", 8080, "")
}

func TestPlanAutoUsesConfiguredBind(t *testing.T) {
	report := PortsReport{SchemaVersion: 1, GeneratedAt: time.Now().UTC(), Listeners: []Listener{{Proto: "tcp", Addr: "0.0.0.0", Port: 8080}}}
	rows := mustPlanAuto(t, VMPlanConfig{HostBind: "0.0.0.0", GuestTargetIP: "192.168.127.3"}, report, nil, PlanOptions{
		HostPortAvailable: func(bind string, port int) (bool, string) {
			if bind != "0.0.0.0" || port != 8080 {
				t.Fatalf("availability checked %s:%d", bind, port)
			}
			return true, ""
		},
	})
	if len(rows) != 1 || rows[0].Bind != "0.0.0.0" {
		t.Fatalf("rows = %#v", rows)
	}
	assertRow(t, rows[0], "active", 8080, "")
}

func TestPlanAutoPrefersForwardableDuplicateGuestPort(t *testing.T) {
	report := PortsReport{SchemaVersion: 1, GeneratedAt: time.Now().UTC(), Listeners: []Listener{
		{Proto: "tcp", Addr: "127.0.0.1", Port: 8080},
		{Proto: "tcp", Addr: "0.0.0.0", Port: 8080},
	}}
	rows := mustPlanAuto(t, VMPlanConfig{GuestTargetIP: "192.168.127.3"}, report, nil, PlanOptions{
		HostPortAvailable: func(string, int) (bool, string) { return true, "" },
	})
	if len(rows) != 1 {
		t.Fatalf("rows = %#v", rows)
	}
	assertRow(t, rows[0], "active", 8080, "")
}

func TestRuntimeStateRoundTripAndPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auto-forwards.json")
	rows := []RuntimeAuto{{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 18080, GuestPort: 8080, GuestTargetIP: "192.168.127.3", Status: "active", Installed: true}}
	if err := WriteRuntimeState(path, rows); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRuntimeState(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].HostPort != 18080 || !got[0].Installed {
		t.Fatalf("unexpected rows: %#v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("state file mode = %o, want 644", info.Mode().Perm())
	}
	if err := WriteRuntimeState(path, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("state file was not removed: %v", err)
	}
}

func TestWriteRuntimeStateReportsRemovalFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "destination")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "child"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteRuntimeState(path, nil); err == nil {
		t.Fatal("expected non-empty directory removal failure")
	}
}

func TestPlanAutoReturnsReservationErrors(t *testing.T) {
	report := PortsReport{Listeners: []Listener{{Proto: "tcp", Addr: "0.0.0.0", Port: 8080}}}
	want := errors.New("reservation read failed")
	_, err := PlanAuto(VMPlanConfig{}, report, nil, PlanOptions{
		PortReserved: func(string, int) (bool, error) { return false, want },
	})
	if !errors.Is(err, want) {
		t.Fatalf("PlanAuto error = %v, want %v", err, want)
	}
}

func TestHostPortUnavailableReason(t *testing.T) {
	if got := HostPortUnavailableReason(80, os.ErrPermission); got != "privileged host port requires permission" {
		t.Fatalf("privileged port reason = %q", got)
	}
	if got := HostPortUnavailableReason(8080, os.ErrPermission); got != "host port is already in use" {
		t.Fatalf("non-privileged port reason = %q", got)
	}
	if got := HostPortUnavailableReason(80, errors.New("other")); got != "host port is already in use" {
		t.Fatalf("default reason = %q", got)
	}
}

func TestHostPortAvailableReportsHeldEphemeralPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("could not bind ephemeral localhost port: %v", err)
	}
	defer func() { _ = ln.Close() }()

	port := ln.Addr().(*net.TCPAddr).Port
	if ok, reason := HostPortAvailable("127.0.0.1", port); ok || !strings.Contains(reason, "already in use") {
		t.Fatalf("HostPortAvailable on held port = %t, %q", ok, reason)
	}
}

func TestValidateReportAndListenerForwardable(t *testing.T) {
	report := PortsReport{SchemaVersion: 1, GeneratedAt: time.Now().UTC(), Listeners: []Listener{{Proto: "tcp", Addr: "0.0.0.0", Port: 8080}}}
	if err := ValidateReport(report); err != nil {
		t.Fatal(err)
	}
	if !ListenerForwardable(report.Listeners[0]) {
		t.Fatal("expected wildcard TCP listener to be forwardable")
	}
	report.Listeners[0].Addr = "127.0.0.1"
	if ListenerForwardable(report.Listeners[0]) {
		t.Fatal("loopback guest listener should not be forwardable")
	}
	report.Listeners[0].Addr = "not-an-ip"
	if err := ValidateReport(report); err == nil || !strings.Contains(err.Error(), "invalid addr") {
		t.Fatalf("expected invalid addr error, got %v", err)
	}
}

func assertRow(t *testing.T, row RuntimeAuto, status string, hostPort int, reason string) {
	t.Helper()
	if row.Status != status || row.HostPort != hostPort || (reason != "" && !strings.Contains(row.Reason, reason)) {
		t.Fatalf("row = %#v, want status=%s hostPort=%d reason~=%q", row, status, hostPort, reason)
	}
}

func mustPlanAuto(t *testing.T, cfg VMPlanConfig, report PortsReport, existing []RuntimeAuto, opts PlanOptions) []RuntimeAuto {
	t.Helper()
	rows, err := PlanAuto(cfg, report, existing, opts)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
