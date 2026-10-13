package forward

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/mjrusso/voom/internal/atomicfile"
)

// Decl is the comparable endpoint identity of a host-to-guest forward.
type Decl struct {
	Protocol  string `json:"protocol"`
	GuestPort int    `json:"guestPort"`
	HostPort  int    `json:"hostPort"`
	Bind      string `json:"bind"`
}

// Row represents a single forward entry as rendered for listing or status output.
type Row struct {
	VM            string `json:"vm,omitempty"`
	Kind          string `json:"kind,omitempty"`
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

// ReservationState records whether a runtime forward is active or skipped.
type ReservationState string

// Persisted reservation states.
const (
	ReservationActive  ReservationState = "active"
	ReservationSkipped ReservationState = "skipped"
)

// RuntimeAuto records a port reservation and its last confirmed backend state.
type RuntimeAuto struct {
	Protocol      string           `json:"protocol"`
	Bind          string           `json:"bind"`
	HostPort      int              `json:"hostPort"`
	GuestPort     int              `json:"guestPort"`
	GuestTargetIP string           `json:"guestTargetIP"`
	Offset        int              `json:"offset"`
	Status        ReservationState `json:"status"`
	Reason        string           `json:"reason,omitempty"`
	GVProxyPID    int              `json:"gvproxyPid,omitempty"`
}

// Endpoint returns the forward identity without runtime metadata.
func (f RuntimeAuto) Endpoint() Decl {
	return Decl{Protocol: f.Protocol, Bind: f.Bind, HostPort: f.HostPort, GuestPort: f.GuestPort}
}

// Installed reports whether backend installation was confirmed.
func (f RuntimeAuto) Installed() bool {
	return f.Status == ReservationActive
}

// MarshalJSON retains the installed field in runtime and CLI JSON.
func (f RuntimeAuto) MarshalJSON() ([]byte, error) {
	type record RuntimeAuto
	return json.Marshal(struct {
		record
		Installed bool `json:"installed"`
	}{record(f), f.Installed()})
}

// UnmarshalJSON accepts legacy installed fields and validates reservation state.
func (f *RuntimeAuto) UnmarshalJSON(data []byte) error {
	type record RuntimeAuto
	var decoded struct {
		record
		Installed *bool `json:"installed"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if decoded.Status == "" {
		decoded.Status = ReservationSkipped
		if decoded.Installed != nil && *decoded.Installed {
			decoded.Status = ReservationActive
		}
	}
	switch decoded.Status {
	case ReservationActive, ReservationSkipped:
	default:
		return fmt.Errorf("unsupported auto-forward reservation state %q", decoded.Status)
	}
	row := RuntimeAuto(decoded.record)
	if decoded.Installed != nil && *decoded.Installed != row.Installed() {
		return errors.New("auto-forward installed field disagrees with reservation state")
	}
	*f = row
	return nil
}

// AutoPlan describes eligibility for a guest listener.
type AutoPlan struct {
	Protocol      string `json:"protocol"`
	Bind          string `json:"bind"`
	HostPort      int    `json:"hostPort"`
	GuestPort     int    `json:"guestPort"`
	GuestTargetIP string `json:"guestTargetIP"`
	Offset        int    `json:"offset"`
	Reason        string `json:"reason,omitempty"`
}

// Endpoint returns the forward identity without planning metadata.
func (p AutoPlan) Endpoint() Decl {
	return Decl{Protocol: p.Protocol, Bind: p.Bind, HostPort: p.HostPort, GuestPort: p.GuestPort}
}

// Preview reports eligible plans as active, regardless of backend installation.
func (p AutoPlan) Preview() Row {
	status := "active"
	if p.Reason != "" {
		status = "skipped"
	}
	return Row{
		Protocol: p.Protocol, Bind: p.Bind, HostPort: p.HostPort,
		GuestPort: p.GuestPort, GuestTargetIP: p.GuestTargetIP, Offset: p.Offset,
		Status: status, Reason: p.Reason, Installed: p.Reason == "",
	}
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
	HostOffset    int
	HostBind      string
	GuestTargetIP string
}

// PlanOptions supplies reservation checks and host-port probes used by PlanAuto.
type PlanOptions struct {
	PortReserved      func(bind string, port int) (bool, error)
	HostPortAvailable func(bind string, port int) (bool, string)
}

// NormalizeProtocol accepts tcp or udp and defaults an empty value to tcp.
func NormalizeProtocol(protocol string) (string, error) {
	if protocol == "" {
		return "tcp", nil
	}
	if protocol != "tcp" && protocol != "udp" {
		return "", fmt.Errorf("unsupported forward protocol %q; use tcp or udp", protocol)
	}
	return protocol, nil
}

// UnmarshalJSON treats an omitted or empty protocol as TCP.
func (d *Decl) UnmarshalJSON(data []byte) error {
	type declaration Decl
	var decoded declaration
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	protocol, err := NormalizeProtocol(decoded.Protocol)
	if err != nil {
		return err
	}
	decoded.Protocol = protocol
	*d = Decl(decoded)
	return nil
}

// Address formats a host address and port, including IPv6 literals.
func Address(bind string, port int) string {
	return net.JoinHostPort(bind, strconv.Itoa(port))
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
	return ProtocolPortAvailable("tcp", bind, port)
}

// ProtocolPortAvailable probes the host socket for the selected transport.
func ProtocolPortAvailable(protocol, bind string, port int) (bool, string) {
	protocol, err := NormalizeProtocol(protocol)
	if err != nil {
		return false, err.Error()
	}
	var socket io.Closer
	if protocol == "udp" {
		socket, err = net.ListenPacket("udp", Address(bind, port))
	} else {
		socket, err = net.Listen("tcp", Address(bind, port))
	}
	if err == nil {
		_ = socket.Close()
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

// PlanAuto computes forwarding candidates from a guest ports report.
func PlanAuto(cfg VMPlanConfig, report PortsReport, existing []RuntimeAuto, opts PlanOptions) ([]AutoPlan, error) {
	hostBind := cfg.HostBind
	if hostBind == "" {
		hostBind = "127.0.0.1"
	}
	if opts.PortReserved == nil {
		opts.PortReserved = func(string, int) (bool, error) { return false, nil }
	}
	if opts.HostPortAvailable == nil {
		opts.HostPortAvailable = HostPortAvailable
	}
	existingActive := map[Decl]struct{}{}
	for _, f := range existing {
		if f.Installed() {
			existingActive[f.Endpoint()] = struct{}{}
		}
	}
	listeners := preferredListeners(report.Listeners)
	rows := make([]AutoPlan, 0, len(listeners))
	for _, l := range listeners {
		hostPort := l.Port + cfg.HostOffset
		f := AutoPlan{Protocol: "tcp", Bind: hostBind, HostPort: hostPort, GuestPort: l.Port, GuestTargetIP: cfg.GuestTargetIP, Offset: cfg.HostOffset}
		_, alreadyActive := existingActive[f.Endpoint()]
		reason, err := autoForwardSkipReason(l, f, alreadyActive, opts)
		if err != nil {
			return nil, err
		}
		if reason != "" {
			f.Reason = reason
		}
		rows = append(rows, f)
	}
	return rows, nil
}

func autoForwardSkipReason(listener Listener, f AutoPlan, alreadyActive bool, opts PlanOptions) (string, error) {
	switch {
	case listener.Proto != "tcp":
		return "non-tcp listener", nil
	case listener.Port == 22:
		return "guest SSH port is reserved", nil
	case !ListenerForwardable(listener):
		return "listener is not bound to all guest interfaces", nil
	case f.HostPort < 1 || f.HostPort > 65535:
		return "calculated host port is outside 1..65535", nil
	}
	reserved, err := opts.PortReserved(f.Bind, f.HostPort)
	if err != nil {
		return "", err
	}
	if reserved {
		return "host port is reserved by SSH or another forward", nil
	}
	if !alreadyActive {
		if ok, reason := opts.HostPortAvailable(f.Bind, f.HostPort); !ok {
			return reason, nil
		}
	}
	return "", nil
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
	for i := range rows {
		protocol, err := NormalizeProtocol(rows[i].Protocol)
		if err != nil {
			return nil, err
		}
		rows[i].Protocol = protocol
	}
	return rows, nil
}

// WriteRuntimeState persists rows to path, removing the file when rows is empty.
func WriteRuntimeState(path string, rows []RuntimeAuto) error {
	if len(rows) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(b, '\n'), 0o644)
}
