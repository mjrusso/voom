package vm

import (
	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
)

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
