package vm

import (
	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
)

type forwardObservation struct {
	running   bool
	installed map[gvproxy.Forward]struct{}
	err       error
}

func (o forwardObservation) status(f forward.Decl, guestTargetIP string) (string, string) {
	switch {
	case !o.running:
		return "stopped", ""
	case o.err != nil:
		return "unavailable", o.err.Error()
	}
	endpoint := gvproxy.Forward{Protocol: f.Protocol, Local: forward.Address(f.Bind, f.HostPort), Remote: forward.Address(guestTargetIP, f.GuestPort)}
	if _, installed := o.installed[endpoint]; !installed {
		return "unavailable", "declared forward is absent from gvproxy"
	}
	return "running", ""
}

func (o forwardObservation) autoStatus(f RuntimeAutoForward) (string, string) {
	if !f.Installed {
		return f.Status, f.Reason
	}
	status, reason := o.status(forward.Decl{Protocol: f.Protocol, Bind: f.Bind, HostPort: f.HostPort, GuestPort: f.GuestPort}, f.GuestTargetIP)
	if status == "running" {
		status = f.Status
	}
	if reason == "" {
		reason = f.Reason
	}
	return status, reason
}
