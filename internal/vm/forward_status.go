package vm

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/state"
)

// ForwardingWarningsForVMs probes a VM list within one deadline, preserving its order.
func (m *Manager) ForwardingWarningsForVMs(ctx context.Context, vms []*state.VMRecord) [][]string {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	warnings := make([][]string, len(vms))
	jobs := make(chan int, len(vms))
	for i := range vms {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for range min(8, len(vms)) {
		workers.Go(func() {
			for i := range jobs {
				if err := ctx.Err(); err != nil {
					warnings[i] = forwardingInspectionWarning(vms[i].Name, err)
					continue
				}
				warnings[i] = m.ForwardingWarnings(ctx, vms[i].Name)
			}
		})
	}
	workers.Wait()
	return warnings
}

// ForwardingWarnings reports pending recovery and unavailable forwards without changing them.
func (m *Manager) ForwardingWarnings(ctx context.Context, name string) []string {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	rows, err := m.ForwardRows(ctx, []string{name})
	if err != nil {
		return forwardingInspectionWarning(name, err)
	}
	var warnings []string
	for _, row := range rows {
		switch row.Status {
		case "uncertain":
			if !slices.Contains(warnings, row.Reason) {
				warnings = append(warnings, row.Reason)
			}
		case "unavailable":
			warnings = append(warnings, fmt.Sprintf("%s %s: %s", row.Protocol, forward.Address(row.Bind, row.HostPort), row.Reason))
		}
	}
	return warnings
}

func forwardingInspectionWarning(name string, err error) []string {
	return []string{fmt.Sprintf("cannot inspect forwarding: %v; run 'voom forward list %s' for details", err, name)}
}

type forwardObservation struct {
	running        bool
	installed      map[gvproxy.Forward]struct{}
	err            error
	recoveryReason string
}

func (o forwardObservation) status(f forward.Decl, guestTargetIP string) (string, string) {
	switch {
	case o.recoveryReason != "":
		return "uncertain", o.recoveryReason
	case o.err != nil:
		return "unavailable", o.err.Error()
	case !o.running:
		return "stopped", ""
	}
	endpoint := gvproxy.Forward{Protocol: f.Protocol, Local: forward.Address(f.Bind, f.HostPort), Remote: forward.Address(guestTargetIP, f.GuestPort)}
	if _, installed := o.installed[endpoint]; !installed {
		return "unavailable", "declared forward is absent from gvproxy"
	}
	return "running", ""
}

func (o forwardObservation) autoStatus(f RuntimeAutoForward) (string, string) {
	if f.Status == forward.ReservationSkipped {
		return string(f.Status), f.Reason
	}
	status, reason := o.status(f.Endpoint(), f.GuestTargetIP)
	if status == "running" {
		status = string(f.Status)
	}
	if reason == "" {
		reason = f.Reason
	}
	return status, reason
}
