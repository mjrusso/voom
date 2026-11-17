package forward

import "errors"

// MutationPhase combines submission, handler outcome, and acknowledgement progress.
type MutationPhase string

// Persisted mutation phases.
const (
	MutationNotSubmitted          MutationPhase = "not-submitted"
	MutationUnknown               MutationPhase = "unknown"
	MutationRejected              MutationPhase = "rejected-before-submission"
	MutationRetired               MutationPhase = "retired"
	MutationCompletedSucceeded    MutationPhase = "completed-succeeded"
	MutationCompletedRejected     MutationPhase = "completed-rejected"
	MutationCompletedFailed       MutationPhase = "completed-failed"
	MutationAcknowledgedSucceeded MutationPhase = "acknowledged-succeeded"
	MutationAcknowledgedRejected  MutationPhase = "acknowledged-rejected"
	MutationAcknowledgedFailed    MutationPhase = "acknowledged-failed"
)

// MutationResult retains the normalized phase and original handler status.
type MutationResult struct {
	Phase  MutationPhase `json:"phase"`
	Status int           `json:"status,omitempty"`
}

// UnsubmittedMutation initializes a request before its first submission.
func UnsubmittedMutation() MutationResult { return MutationResult{Phase: MutationNotSubmitted} }

// UnknownMutation retains a submission whose outcome has not been confirmed.
func UnknownMutation() MutationResult { return MutationResult{Phase: MutationUnknown} }

// RejectedMutation records a rejection before submission.
func RejectedMutation() MutationResult { return MutationResult{Phase: MutationRejected} }

// RetiredMutation records an ID that cannot execute again but has no cached outcome.
func RetiredMutation() MutationResult { return MutationResult{Phase: MutationRetired} }

// Acknowledged records release of a durably saved completion at the helper.
func (r MutationResult) Acknowledged() MutationResult {
	switch r.Phase {
	case MutationCompletedSucceeded:
		r.Phase = MutationAcknowledgedSucceeded
	case MutationCompletedRejected:
		r.Phase = MutationAcknowledgedRejected
	case MutationCompletedFailed:
		r.Phase = MutationAcknowledgedFailed
	}
	return r
}

// After preserves an earlier uncertain submission when the retry is not submitted.
func (r MutationResult) After(previous MutationResult) MutationResult {
	if previous.Phase == MutationUnknown && r.Phase == MutationRejected {
		return UnknownMutation()
	}
	return r
}

// CompletedMutation classifies a handler response. A conflict rejects expose,
// but an unexpose conflict may follow a partial close and requires recovery.
func CompletedMutation(exposes bool, status int) MutationResult {
	phase := MutationCompletedFailed
	switch {
	case status >= 200 && status < 300:
		phase = MutationCompletedSucceeded
	case status == 400 || status == 429 || (exposes && status == 409):
		phase = MutationCompletedRejected
	}
	return MutationResult{Phase: phase, Status: status}
}

func (r MutationResult) validate(exposes bool) error {
	switch r.Phase {
	case MutationNotSubmitted, MutationUnknown, MutationRejected, MutationRetired:
		if r.Status != 0 {
			return errors.New("uncompleted mutation has an HTTP status")
		}
	case MutationCompletedSucceeded, MutationCompletedRejected, MutationCompletedFailed,
		MutationAcknowledgedSucceeded, MutationAcknowledgedRejected, MutationAcknowledgedFailed:
		if r.Status < 200 || r.Status > 599 {
			return errors.New("invalid mutation HTTP status")
		}
		expected := CompletedMutation(exposes, r.Status)
		if r.Phase != expected.Phase && r.Phase != expected.Acknowledged().Phase {
			return errors.New("mutation phase contradicts its HTTP status")
		}
	default:
		return errors.New("invalid mutation phase")
	}
	return nil
}
