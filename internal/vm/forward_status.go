package vm

import (
	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
)

type forwardObservation struct {
	running   bool
	uncertain bool
	installed map[gvproxy.Forward]struct{}
	err       error
}

func (o forwardObservation) status(f forward.Decl, guestTargetIP string) (string, string) {
	switch {
	case o.uncertain:
		return "uncertain", "forward mutation requires confirmed gvproxy termination"
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
