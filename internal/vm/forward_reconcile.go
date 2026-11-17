package vm

import (
	"context"
	"errors"
	"os"
	"slices"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

func mutationEndpoint(request forward.MutationRequest) gvproxy.Forward {
	endpoint := request.Endpoint
	remote := ""
	if request.Action() == "expose" {
		remote = forward.Address(request.GuestTargetIP, endpoint.GuestPort)
	}
	return gvproxy.Forward{Protocol: endpoint.Protocol, Local: forward.Address(endpoint.Bind, endpoint.HostPort), Remote: remote}
}

// Save completion before acknowledging it, and save acknowledgement progress so
// a recovery deadline cannot force every attempt to repeat the same prefix.
func (m *Manager) settleForwardRequest(ctx context.Context, vm *state.VMRecord, journal *forward.MutationJournal, request *forward.MutationRequest) error {
	path := m.store.Runtime(vm).ForwardMutation()
	if forward.NextRecovery(*request, nil).Step == forward.RecoverySubmit {
		previous := request.Result
		request.Result = forward.UnknownMutation()
		if err := state.WriteJSONAtomic(path, journal); err != nil {
			return err
		}
		result, err := gvproxy.ChangeForward(ctx, m.store.Runtime(vm).NetworkSock(), request.Action(), mutationEndpoint(*request), request.ID)
		request.Result = result.After(previous)
		request.Error = ""
		if err != nil {
			request.Error = err.Error()
		}
		if err := state.WriteJSONAtomic(path, journal); err != nil {
			return err
		}
	}
	if forward.NextRecovery(*request, nil).Step == forward.RecoverySubmit {
		return errors.Join(gvproxy.ErrForwardOutcomeUnknown, errors.New(request.Error))
	}
	if forward.NextRecovery(*request, nil).Step == forward.RecoveryAcknowledge {
		if err := gvproxy.AcknowledgeForward(ctx, m.store.Runtime(vm).NetworkSock(), request.ID); err != nil {
			return err
		}
		request.Result = request.Result.Acknowledged()
		if err := state.WriteJSONAtomic(path, journal); err != nil {
			return err
		}
	}
	return nil
}

// All submitted requests must be fenced before a forwarding snapshot can be trusted.
// A completed journal can still report confirmed rejections.
func (m *Manager) processForwardJournal(ctx context.Context, vm *state.VMRecord, journal *forward.MutationJournal) (bool, error) {
	if len(journal.Requests) == 0 {
		err := m.commitForwardJournal(ctx, vm, *journal, nil)
		return err == nil, err
	}
	for round := 0; ; round++ {
		for i := range journal.Requests {
			if err := ctx.Err(); err != nil {
				return false, errors.Join(errForwardRecoveryRequired, err)
			}
			if err := m.settleForwardRequest(ctx, vm, journal, &journal.Requests[i]); err != nil {
				return false, errors.Join(errForwardRecoveryRequired, err)
			}
		}
		installed, err := gvproxy.Forwards(ctx, m.store.Runtime(vm).NetworkSock())
		if err != nil {
			return false, errors.Join(errForwardRecoveryRequired, err)
		}
		var repairs []int
		var failures []error
		var resolutions []forwardResolution
		for i := range journal.Requests {
			request := &journal.Requests[i]
			matches := forwardRequestMatches(*request, installed)
			transition := forward.NextRecovery(*request, &matches)
			*request = transition.Request
			switch transition.Step {
			case forward.RecoveryComplete:
			case forward.RecoveryRepair:
				if round == 0 {
					repairs = append(repairs, i)
					continue
				}
				if err := state.WriteJSONAtomic(m.store.Runtime(vm).ForwardMutation(), journal); err != nil {
					return false, errors.Join(errForwardRecoveryRequired, err)
				}
				return false, errors.Join(errForwardRecoveryRequired, errors.New(request.Error))
			case forward.RecoveryReject:
				if transition.ReportFailure {
					failures = append(failures, &forwardMutationFailure{request: *request, err: errors.New(request.Error)})
				}
			default:
				return false, errors.Join(errForwardRecoveryRequired, errors.New("forwarding request is not settled"))
			}
			outcome := forwardDeclarationConfirmed
			if transition.UndoDeclaration {
				outcome = forwardDeclarationRejected
			}
			resolutions = append(resolutions, forwardResolution{request: *request, outcome: outcome})
		}
		if len(repairs) > 0 {
			ids, err := gvproxy.AllocateForwardIDs(ctx, m.store.Runtime(vm).NetworkSock(), len(repairs))
			if err != nil {
				return false, errors.Join(errForwardRecoveryRequired, err)
			}
			for i, index := range repairs {
				request := &journal.Requests[index]
				*request = request.Repair(ids[i])
			}
			if err := state.WriteJSONAtomic(m.store.Runtime(vm).ForwardMutation(), journal); err != nil {
				return false, errors.Join(errForwardRecoveryRequired, err)
			}
			continue
		}
		if err := m.commitForwardJournal(ctx, vm, *journal, resolutions); err != nil {
			return false, errors.Join(errForwardRecoveryRequired, err)
		}
		if err := m.finishForwardJournal(ctx, vm); err != nil {
			return false, errors.Join(errForwardRecoveryRequired, err)
		}
		return true, errors.Join(failures...)
	}
}

func (m *Manager) finishForwardJournal(ctx context.Context, vm *state.VMRecord) error {
	return m.store.WithGlobalVMContext(ctx, vm.Name, vm.ID, func() error {
		return os.Remove(m.store.Runtime(vm).ForwardMutation())
	})
}

// An uncertain request must finish or retire before the forwarding table is safe to trust.
func (m *Manager) resolvePendingForwardMutation(ctx context.Context, vm *state.VMRecord) (bool, error) {
	rt := m.store.Runtime(vm)
	journal, err := forward.ReadMutationJournal(rt.ForwardMutation())
	if err != nil {
		return false, err
	}
	if journal == nil {
		return true, nil
	}
	if journal.Version != 1 {
		return false, errors.New("recovery details are unavailable; stop and start the VM")
	}
	observed, err := process.ObserveRecord(rt.GVProxyProcessRecord(), "gvproxy", journal.Process)
	if err != nil {
		return false, err
	}
	if observed.State != process.RecordRunning {
		return false, errors.New("the original gvproxy process cannot be verified")
	}
	stored, err := m.store.LoadVMByID(vm.ID)
	if err != nil {
		return false, err
	}
	vm.Network = stored.Network
	if err := m.store.WithGlobalVMContext(ctx, vm.Name, vm.ID, func() error { return m.saveManualDeclaration(vm, journal.Manual) }); err != nil {
		return false, err
	}
	if slices.ContainsFunc(journal.Requests, func(request forward.MutationRequest) bool {
		return request.Result.Phase == forward.MutationUnknown
	}) {
		if err := gvproxy.CheckProcess(ctx, rt.NetworkSock(), gvproxy.ForwardRecoveryCapability); err != nil {
			return false, err
		}
	}
	return m.processForwardJournal(ctx, vm, journal)
}

func forwardRequestMatches(request forward.MutationRequest, installed map[gvproxy.Forward]struct{}) bool {
	endpoint := mutationEndpoint(request)
	if request.Action() == "expose" {
		_, present := installed[endpoint]
		return present
	}
	for row := range installed {
		if row.Protocol == endpoint.Protocol && row.Local == endpoint.Local {
			return false
		}
	}
	return true
}

// ReconcileForwards confirms pending requests and applies saved forwarding state without restarting the VM.
func (m *Manager) ReconcileForwards(ctx context.Context, name string) ([]ForwardRow, error) {
	vm, unlock, err := m.store.LockVMRecord(ctx, name)
	if err != nil {
		return nil, err
	}
	err = m.reconcileForwardsLocked(ctx, vm)
	unlock()
	rows, readErr := m.ForwardRows(ctx, []string{name})
	return rows, errors.Join(err, readErr)
}

func (m *Manager) reconcileForwardsLocked(ctx context.Context, vm *state.VMRecord) error {
	if err := m.recoverPendingForwardMutation(ctx, vm); err != nil {
		return err
	}
	if !m.IsRunning(vm) {
		return nil
	}
	installed, err := gvproxy.Forwards(ctx, m.store.Runtime(vm).NetworkSock())
	if err != nil {
		return err
	}
	var requests []forward.MutationRequest
	guestTargetIP := m.GuestTargetIP(vm)
	for _, declaration := range declaredForwards(vm) {
		endpoint := gvproxy.Forward{Protocol: declaration.Protocol, Local: forward.Address(declaration.Bind, declaration.HostPort), Remote: forward.Address(guestTargetIP, declaration.GuestPort)}
		if _, present := installed[endpoint]; !present {
			requests = append(requests, forward.MutationRequest{Kind: forward.ManualExpose, Endpoint: declaration, GuestTargetIP: guestTargetIP})
		}
	}
	_, err = m.applyForwardDeclarations(ctx, vm, forward.MutationJournal{Requests: requests})
	if _, err := m.recoverAndReportForwardMutation(ctx, vm, err); err != nil {
		return err
	}
	if vm.Network.AutoForward {
		_, err = m.reconcileAutoForwardsLocked(ctx, vm)
	} else {
		err = m.cleanupAutoForwardsLocked(ctx, vm)
	}
	_, err = m.recoverAndReportForwardMutation(ctx, vm, err)
	return err
}
