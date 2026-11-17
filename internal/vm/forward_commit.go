package vm

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/state"
)

type forwardDeclarationOutcome uint8

const (
	forwardDeclarationConfirmed forwardDeclarationOutcome = iota
	forwardDeclarationRejected
)

type forwardResolution struct {
	request forward.MutationRequest
	outcome forwardDeclarationOutcome
}

// The caller must hold the global lock. Save declarations before submitting requests.
func (m *Manager) saveManualDeclaration(vm *state.VMRecord, intent *forward.ManualMutation) error {
	if intent == nil {
		return nil
	}
	previous := vm.Network.Forwards
	next := slices.Clone(previous)
	if intent.Kind == forward.ManualExpose && !slices.Contains(next, intent.Endpoint) {
		next = append(next, intent.Endpoint)
	} else if intent.Kind == forward.ManualUnexpose {
		next = slices.DeleteFunc(next, func(endpoint forward.Decl) bool { return endpoint == intent.Endpoint })
	}
	if slices.Equal(previous, next) {
		return nil
	}
	vm.Network.Forwards = next
	vm.UpdatedAt = time.Now().UTC()
	if err := m.store.SaveVM(vm); err != nil {
		vm.Network.Forwards = previous
		return errors.Join(err, m.store.SaveVM(vm))
	}
	return nil
}

func (m *Manager) commitForwardJournal(ctx context.Context, vm *state.VMRecord, journal forward.MutationJournal, resolutions []forwardResolution) error {
	if intent := journal.Manual; intent != nil {
		if resolutions[0].outcome == forwardDeclarationRejected {
			rollback := *intent
			if intent.Kind == forward.ManualExpose {
				rollback.Kind = forward.ManualUnexpose
			} else {
				rollback.Kind = forward.ManualExpose
			}
			if err := m.store.WithGlobalVMContext(ctx, vm.Name, vm.ID, func() error { return m.saveManualDeclaration(vm, &rollback) }); err != nil {
				return err
			}
		}
	}

	if journal.AutoForwards == nil {
		return nil
	}
	oldRows, err := m.ReadRuntimeAutoForwards(vm)
	if err != nil {
		return err
	}
	rows := slices.Clone(*journal.AutoForwards)
	for _, resolution := range resolutions {
		request := resolution.request
		if request.Exposes() || resolution.outcome != forwardDeclarationRejected {
			continue
		}
		index := slices.IndexFunc(oldRows, func(row RuntimeAutoForward) bool { return row.Endpoint() == request.Endpoint })
		if index < 0 {
			return errors.New("cannot retain reservation for a rejected automatic removal")
		}
		if !slices.ContainsFunc(rows, func(row RuntimeAutoForward) bool { return row.Endpoint() == request.Endpoint }) {
			rows = append(rows, oldRows[index])
		}
	}
	exposures := make(map[forward.Decl]forwardResolution)
	for _, resolution := range resolutions {
		if resolution.request.Exposes() {
			exposures[resolution.request.Endpoint] = resolution
		}
	}
	for i := range rows {
		row := &rows[i]
		if !row.Installed() {
			continue
		}
		resolution, evaluated := exposures[row.Endpoint()]
		if !evaluated {
			continue
		}
		switch resolution.outcome {
		case forwardDeclarationConfirmed:
			row.Reason = ""
		case forwardDeclarationRejected:
			row.Status, row.GVProxyPID, row.Reason = forward.ReservationSkipped, 0, "gvproxy expose failed: "+resolution.request.Error
		}
	}
	if err := m.saveRuntimeAutoForwards(vm, rows); err != nil {
		return err
	}
	m.emitAutoForwardTransitions(vm, oldRows, rows)
	return nil
}
