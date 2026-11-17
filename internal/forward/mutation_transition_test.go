package forward

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRecoveryTransitions(t *testing.T) {
	success := MutationResult{Phase: MutationCompletedSucceeded, Status: 200}
	failed := (MutationResult{Phase: MutationCompletedFailed, Status: 500}).Acknowledged()
	rejected := (MutationResult{Phase: MutationCompletedRejected, Status: 409}).Acknowledged()
	present, absent := true, false
	for _, tc := range []struct {
		name         string
		kind         MutationKind
		result       MutationResult
		matches      *bool
		step         RecoveryStep
		undo, report bool
	}{
		{"unsubmitted", ManualExpose, UnsubmittedMutation(), nil, RecoverySubmit, false, false},
		{"uncertain submission", ManualExpose, UnknownMutation(), nil, RecoverySubmit, false, false},
		{"completion must be acknowledged", ManualExpose, success, nil, RecoveryAcknowledge, false, false},
		{"acknowledged batch needs observation", ManualExpose, success.Acknowledged(), nil, RecoveryObserve, false, false},
		{"matching listener completes", ManualExpose, success.Acknowledged(), &present, RecoveryComplete, false, false},
		{"lost listener needs repair", ManualExpose, success.Acknowledged(), &absent, RecoveryRepair, false, false},
		{"retired missing listener needs repair", ManualExpose, RetiredMutation(), &absent, RecoveryRepair, false, false},
		{"retired matching listener completes", ManualExpose, RetiredMutation(), &present, RecoveryComplete, false, false},
		{"manual rejection undoes declaration", ManualExpose, rejected, &absent, RecoveryReject, true, true},
		{"automatic expose rejection skips row", AutomaticExpose, rejected, &absent, RecoveryReject, true, false},
		{"automatic removal rejection retains row", AutomaticUnexpose, RejectedMutation(), &absent, RecoveryReject, true, true},
		{"failed expose can be settled by observation", ManualExpose, failed, &present, RecoveryComplete, false, false},
		{"failed close cannot be settled by absence", ManualUnexpose, failed, &present, RecoveryRepair, false, false},
		{"local retry rejection preserves earlier uncertainty", ManualExpose, RejectedMutation().After(UnknownMutation()), &present, RecoverySubmit, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NextRecovery(MutationRequest{Kind: tc.kind, Result: tc.result}, tc.matches)
			if got.Step != tc.step || got.UndoDeclaration != tc.undo || got.ReportFailure != tc.report {
				t.Fatalf("transition = %+v; want step %d, undo %t, report %t", got, tc.step, tc.undo, tc.report)
			}
		})
	}
}

func TestFailedCloseSurvivesReplacementAndReload(t *testing.T) {
	failed := MutationResult{Phase: MutationCompletedFailed, Status: 500}
	request := MutationRequest{ID: "first", Kind: ManualUnexpose, Endpoint: Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 8080, GuestPort: 80}, Result: failed.Acknowledged()}
	absent := true
	request = NextRecovery(request, &absent).Request.Repair("replacement")
	request.Result = RejectedMutation()
	data, err := json.Marshal(MutationJournal{Version: 1, Process: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Requests: []MutationRequest{request}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "journal")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	journal, err := ReadMutationJournal(path)
	if err != nil || journal == nil {
		t.Fatalf("reload: %+v %v", journal, err)
	}
	request = journal.Requests[0]
	if got := NextRecovery(request, &absent); got.Step != RecoveryRepair {
		t.Fatalf("rejected replacement released an unconfirmed close: %+v", got)
	}
	request.Result = (MutationResult{Phase: MutationCompletedSucceeded, Status: 200}).Acknowledged()
	got := NextRecovery(request, &absent)
	if got.Step != RecoveryComplete || got.Request.closePending() {
		t.Fatalf("confirmed close did not complete: %+v", got)
	}
}
