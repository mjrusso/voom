package forward

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Decl declares a host-to-guest port forward configured for a VM.
type Decl struct {
	Protocol  string `json:"protocol"`
	GuestPort int    `json:"guestPort"`
	HostPort  int    `json:"hostPort"`
	Bind      string `json:"bind"`
}

// Row represents a single forward entry as rendered for listing or status output.
type Row struct {
	VM            string `json:"vm"`
	Kind          string `json:"kind"`
	Bind          string `json:"bind"`
	HostPort      int    `json:"hostPort"`
	GuestPort     int    `json:"guestPort"`
	GuestTargetIP string `json:"guestTargetIP"`
	Protocol      string `json:"protocol"`
	Status        string `json:"status"`
	Installed     bool   `json:"installed"`
	Offset        int    `json:"offset"`
	Reason        string `json:"reason,omitempty"`
}

// RuntimeAuto is a runtime auto-forward entry mirroring a guest listener on the host.
type RuntimeAuto struct {
	Protocol      string `json:"protocol"`
	Bind          string `json:"bind"`
	HostPort      int    `json:"hostPort"`
	GuestPort     int    `json:"guestPort"`
	GuestTargetIP string `json:"guestTargetIP"`
	Offset        int    `json:"offset"`
	Status        string `json:"status"`
	Reason        string `json:"reason,omitempty"`
	Installed     bool   `json:"installed"`
	GVProxyPID    int    `json:"gvproxyPid,omitempty"`
}

// Listener describes a single TCP listener observed inside the guest.
type Listener struct {
	Proto   string `json:"proto"`
	Addr    string `json:"addr"`
	Port    int    `json:"port"`
	PID     int    `json:"pid,omitempty"`
	Process string `json:"process,omitempty"`
}

// PortsReport is the report of guest listeners published by the guest agent.
type PortsReport struct {
	SchemaVersion int        `json:"schemaVersion"`
	GeneratedAt   time.Time  `json:"generatedAt"`
	Listeners     []Listener `json:"listeners"`
}

// VMPlanConfig holds per-VM settings consumed by PlanAuto.
type VMPlanConfig struct {
	VMID          string
	AutoForward   bool
	HostOffset    int
	HostBind      string
	GuestTargetIP string
}

// PlanOptions supplies reservation checks and host-port probes used by PlanAuto.
type PlanOptions struct {
	PortReserved        func(bind string, port int) bool
	RuntimeAutoReserved func(bind string, port int) bool
	HostPortAvailable   func(bind string, port int) (bool, string)
}

// NormalizeBind validates and canonicalizes a bind address as an IP literal.
func NormalizeBind(bind string) (string, error) {
	if bind == "localhost" {
		return "", errors.New("localhost is not a valid persisted bind address; use 127.0.0.1 or ::1")
	}
	ip := net.ParseIP(bind)
	if ip == nil {
		return "", fmt.Errorf("bind address must be an IP literal: %s", bind)
	}
	return ip.String(), nil
}

// BindsConflict reports whether two bind addresses overlap and cannot share a host port.
func BindsConflict(a, b string) bool {
	if a == b {
		return true
	}
	aIP := net.ParseIP(a)
	bIP := net.ParseIP(b)
	if aIP == nil || bIP == nil {
		return false
	}
	a4 := aIP.To4() != nil
	b4 := bIP.To4() != nil
	if a == "::" || b == "::" {
		return true
	}
	if a4 || b4 {
		if !a4 || !b4 {
			return false
		}
		return a == "0.0.0.0" || b == "0.0.0.0"
	}
	return false
}

// HostPortAvailable probes whether the given bind/port can be opened, returning a reason if not.
func HostPortAvailable(bind string, port int) (bool, string) {
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(port)))
	if err == nil {
		_ = ln.Close()
		return true, ""
	}
	return false, HostPortUnavailableReason(port, err)
}

// HostPortUnavailableReason maps a listen error to a human-readable reason string.
func HostPortUnavailableReason(port int, err error) string {
	if port < 1024 && errors.Is(err, os.ErrPermission) {
		return "privileged host port requires permission"
	}
	return "host port is already in use"
}

// ListenerForwardable reports whether a guest listener is eligible for auto-forwarding.
func ListenerForwardable(l Listener) bool {
	return l.Proto == "tcp" && l.Port != 22 && (l.Addr == "0.0.0.0" || l.Addr == "::" || l.Addr == "*" || l.Addr == "")
}

// ValidateReport checks a PortsReport for schema, freshness, and listener validity.
func ValidateReport(r PortsReport) error {
	if r.SchemaVersion != 1 {
		return errors.New("unsupported guest port report schema")
	}
	if r.GeneratedAt.IsZero() {
		return errors.New("guest port report is missing generatedAt")
	}
	if time.Since(r.GeneratedAt) > 30*time.Second {
		return errors.New("guest port report is stale")
	}
	for i, l := range r.Listeners {
		if l.Proto != "tcp" {
			return fmt.Errorf("guest port report listener %d has unsupported proto %q", i, l.Proto)
		}
		if l.Port < 1 || l.Port > 65535 {
			return fmt.Errorf("guest port report listener %d has invalid port %d", i, l.Port)
		}
		if l.Addr != "" && l.Addr != "*" && net.ParseIP(l.Addr) == nil {
			return fmt.Errorf("guest port report listener %d has invalid addr %q", i, l.Addr)
		}
	}
	return nil
}

// PlanAuto computes the desired runtime auto-forward rows for a VM from a guest ports report.
func PlanAuto(cfg VMPlanConfig, report *PortsReport, existing []RuntimeAuto, opts PlanOptions) []RuntimeAuto {
	if !cfg.AutoForward || report == nil {
		return nil
	}
	hostBind := cfg.HostBind
	if hostBind == "" {
		hostBind = "127.0.0.1"
	}
	if opts.PortReserved == nil {
		opts.PortReserved = func(string, int) bool { return false }
	}
	if opts.RuntimeAutoReserved == nil {
		opts.RuntimeAutoReserved = func(string, int) bool { return false }
	}
	if opts.HostPortAvailable == nil {
		opts.HostPortAvailable = HostPortAvailable
	}
	existingActive := map[string]struct{}{}
	for _, f := range existing {
		if f.Installed {
			existingActive[Key(f)] = struct{}{}
		}
	}
	listeners := preferredListeners(report.Listeners)
	rows := make([]RuntimeAuto, 0, len(listeners))
	for _, l := range listeners {
		hostPort := l.Port + cfg.HostOffset
		f := RuntimeAuto{Protocol: "tcp", Bind: hostBind, HostPort: hostPort, GuestPort: l.Port, GuestTargetIP: cfg.GuestTargetIP, Offset: cfg.HostOffset, Status: "active", Installed: true}
		_, alreadyActive := existingActive[Key(f)]
		switch {
		case l.Proto != "tcp":
			f.Status, f.Installed, f.Reason = "skipped", false, "non-tcp listener"
		case l.Port == 22:
			f.Status, f.Installed, f.Reason = "skipped", false, "guest SSH port is reserved"
		case !ListenerForwardable(l):
			f.Status, f.Installed, f.Reason = "skipped", false, "listener is not bound to all guest interfaces"
		case hostPort < 1 || hostPort > 65535:
			f.Status, f.Installed, f.Reason = "skipped", false, "calculated host port is outside 1..65535"
		case opts.PortReserved(f.Bind, f.HostPort):
			f.Status, f.Installed, f.Reason = "skipped", false, "host port is reserved by SSH or a manual forward"
		case opts.RuntimeAutoReserved(f.Bind, f.HostPort):
			f.Status, f.Installed, f.Reason = "skipped", false, "host port is already used by another runtime auto-forward"
		case !alreadyActive:
			if ok, reason := opts.HostPortAvailable(f.Bind, f.HostPort); !ok {
				f.Status, f.Installed, f.Reason = "skipped", false, reason
			}
		}
		rows = append(rows, f)
	}
	return rows
}

func preferredListeners(listeners []Listener) []Listener {
	byPort := map[int]Listener{}
	order := []int{}
	for _, l := range listeners {
		if _, ok := byPort[l.Port]; !ok {
			order = append(order, l.Port)
			byPort[l.Port] = l
			continue
		}
		if ListenerForwardable(l) && !ListenerForwardable(byPort[l.Port]) {
			byPort[l.Port] = l
		}
	}
	out := make([]Listener, 0, len(order))
	for _, port := range order {
		out = append(out, byPort[port])
	}
	return out
}

// Key returns a stable identity string for a RuntimeAuto row.
func Key(f RuntimeAuto) string {
	return fmt.Sprintf("%s/%s/%d/%d", f.Protocol, f.Bind, f.HostPort, f.GuestPort)
}

// RuntimeStatePath returns the path to the persisted auto-forward state file for a VM.
func RuntimeStatePath(runtimeVMDir string) string {
	return filepath.Join(runtimeVMDir, "auto-forwards.json")
}

// WatcherPidfile returns the path to the auto-forward watcher pidfile for a VM.
func WatcherPidfile(runtimeVMDir string) string {
	return filepath.Join(runtimeVMDir, "auto-forward.pid")
}

// ReadRuntimeState loads the persisted runtime auto-forward rows from path.
func ReadRuntimeState(path string) ([]RuntimeAuto, error) {
	var rows []RuntimeAuto
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, fmt.Errorf("runtime auto-forward state at %s is malformed: %w", path, err)
	}
	return rows, nil
}

// WriteRuntimeState persists rows to path, removing the file when rows is empty.
func WriteRuntimeState(path string, rows []RuntimeAuto) error {
	if len(rows) == 0 {
		_ = os.Remove(path)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
