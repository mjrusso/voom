package forward

import (
	"errors"
	"fmt"
	"net/netip"
)

// MutationKind combines the operation and declaration owner. Failed closes have
// separate internal kinds so replacement IDs cannot discard that obligation.
type MutationKind string

// Supported forwarding intents.
const (
	ManualExpose      MutationKind = "manual-expose"
	ManualUnexpose    MutationKind = "manual-unexpose"
	AutomaticExpose   MutationKind = "automatic-expose"
	AutomaticUnexpose MutationKind = "automatic-unexpose"
	manualClosing     MutationKind = "manual-close-pending"
	automaticClosing  MutationKind = "automatic-close-pending"
)

// Automatic transfers an intent to the automatic forwarding plan.
func (k MutationKind) Automatic() MutationKind {
	switch k {
	case ManualExpose:
		return AutomaticExpose
	case ManualUnexpose:
		return AutomaticUnexpose
	case manualClosing:
		return automaticClosing
	default:
		return k
	}
}

// MutationRequest retains a request's identity and normalized recovery state.
type MutationRequest struct {
	ID            string         `json:"id"`
	Kind          MutationKind   `json:"kind"`
	Endpoint      Decl           `json:"endpoint"`
	GuestTargetIP string         `json:"guestTargetIP,omitempty"`
	Result        MutationResult `json:"result"`
	Error         string         `json:"error,omitempty"`
}

// Action returns the helper's operation name.
func (r MutationRequest) Action() string {
	if r.Exposes() {
		return "expose"
	}
	return "unexpose"
}

// Exposes reports whether the request installs a listener.
func (r MutationRequest) Exposes() bool { return r.Kind == ManualExpose || r.Kind == AutomaticExpose }

// Automatic reports whether an automatic plan owns the declaration.
func (r MutationRequest) Automatic() bool {
	return r.Kind == AutomaticExpose || r.Kind == AutomaticUnexpose || r.Kind == automaticClosing
}

func (r MutationRequest) closePending() bool {
	return r.Kind == manualClosing || r.Kind == automaticClosing
}

// Repair assigns a fresh request ID while preserving any unconfirmed close.
func (r MutationRequest) Repair(id string) MutationRequest {
	r.ID, r.Result, r.Error = id, UnsubmittedMutation(), ""
	return r
}

func (r MutationRequest) validate() error {
	if len(r.ID) == 0 || len(r.ID) > 64 {
		return errors.New("invalid recovery request identity")
	}
	switch r.Kind {
	case ManualExpose, ManualUnexpose, AutomaticExpose, AutomaticUnexpose, manualClosing, automaticClosing:
	default:
		return errors.New("invalid recovery request kind")
	}
	if r.Exposes() {
		if _, err := netip.ParseAddr(r.GuestTargetIP); err != nil {
			return err
		}
	}
	if _, err := NormalizeBind(r.Endpoint.Bind); err != nil {
		return err
	}
	if r.Endpoint.HostPort < 1 || r.Endpoint.HostPort > 65535 || r.Endpoint.GuestPort < 1 || r.Endpoint.GuestPort > 65535 {
		return errors.New("invalid recovery endpoint")
	}
	return r.Result.validate(r.Exposes())
}

// RecoveryStep is the next operation required by a normalized request.
type RecoveryStep uint8

// Recovery operations; persistence and HTTP execution belong to the caller.
const (
	RecoverySubmit RecoveryStep = iota
	RecoveryAcknowledge
	RecoveryObserve
	RecoveryRepair
	RecoveryReject
	RecoveryComplete
)

// RecoveryTransition contains the next request state and declaration decision.
type RecoveryTransition struct {
	Request         MutationRequest
	Step            RecoveryStep
	UndoDeclaration bool
	ReportFailure   bool
}

// NextRecovery chooses submission, repair, or completion without performing I/O.
// A nil snapshot means the batch has not yet been fenced and observed.
func NextRecovery(r MutationRequest, matches *bool) RecoveryTransition {
	t := RecoveryTransition{Request: r}
	switch r.Result.Phase {
	case MutationNotSubmitted, MutationUnknown:
		t.Step = RecoverySubmit
		return t
	case MutationCompletedSucceeded, MutationCompletedRejected, MutationCompletedFailed:
		t.Step = RecoveryAcknowledge
		return t
	}
	if matches == nil {
		t.Step = RecoveryObserve
		return t
	}
	if !r.Exposes() && r.Result.Phase == MutationAcknowledgedFailed {
		if r.Automatic() {
			r.Kind = automaticClosing
		} else {
			r.Kind = manualClosing
		}
	}
	switch r.Result.Phase {
	case MutationRejected:
		t.Step = RecoveryReject
	case MutationRetired, MutationAcknowledgedSucceeded:
		t.Step = RecoveryRepair
		if *matches {
			t.Step = RecoveryComplete
		}
	case MutationAcknowledgedRejected, MutationAcknowledgedFailed:
		t.Step = RecoveryReject
		if *matches {
			t.Step = RecoveryComplete
		}
	}
	if r.closePending() && r.Result.Phase != MutationAcknowledgedSucceeded {
		t.Step = RecoveryRepair
	}
	switch t.Step {
	case RecoveryComplete:
		r.Error = ""
		if r.Kind == manualClosing {
			r.Kind = ManualUnexpose
		}
		if r.Kind == automaticClosing {
			r.Kind = AutomaticUnexpose
		}
	case RecoveryRepair:
		if r.Error == "" {
			r.Error = "forwarding table does not match the completed request"
		}
	case RecoveryReject:
		if r.Error == "" {
			r.Error = fmt.Sprintf("gvproxy %s returned HTTP %d", r.Action(), r.Result.Status)
		}
		t.ReportFailure = !r.Automatic() || !r.Exposes()
		t.UndoDeclaration = !*matches
	}
	t.Request = r
	return t
}
