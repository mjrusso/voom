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

// RuntimeInspection reports VM status and forwarding warnings from the same
// driver process observation.
type RuntimeInspection struct {
	Status             RuntimeState
	RuntimeError       error
	ForwardingWarnings []string
}

// InspectRuntimes checks VM processes first so slow forwarding probes cannot
// use up the shared deadline before a VM's process is checked. Results follow
// the input order.
func (m *Manager) InspectRuntimes(ctx context.Context, vms []*state.VMRecord) []RuntimeInspection {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	results := make([]RuntimeInspection, len(vms))
	inspectBatch(len(vms), func(i int) {
		results[i].Status, results[i].RuntimeError = m.ObserveRuntimeContext(ctx, vms[i])
	})
	inspectBatch(len(vms), func(i int) {
		result := &results[i]
		result.ForwardingWarnings = m.forwardingWarnings(ctx, vms[i], result.Status, result.RuntimeError)
	})
	return results
}

func inspectBatch(count int, inspect func(int)) {
	jobs := make(chan int, count)
	for i := range count {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for range min(8, count) {
		workers.Go(func() {
			for i := range jobs {
				inspect(i)
			}
		})
	}
	workers.Wait()
}

// InspectRuntime bounds the driver and forwarding inspection with one deadline.
func (m *Manager) InspectRuntime(ctx context.Context, vm *state.VMRecord) RuntimeInspection {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	status, runtimeErr := m.ObserveRuntimeContext(ctx, vm)
	return RuntimeInspection{
		Status:             status,
		RuntimeError:       runtimeErr,
		ForwardingWarnings: m.forwardingWarnings(ctx, vm, status, runtimeErr),
	}
}

func (m *Manager) forwardingWarnings(ctx context.Context, vm *state.VMRecord, status RuntimeState, runtimeErr error) []string {
	rows, err := m.forwardRowsForVM(ctx, vm, status, runtimeErr)
	if err != nil {
		return []string{fmt.Sprintf("cannot inspect forwarding: %v. Run 'voom forward list %s' for details", err, vm.Name)}
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
