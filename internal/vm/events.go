package vm

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/mjrusso/voom/internal/events"
	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/state"
)

func (m *Manager) recordForwardRecovery(vm *state.VMRecord, outcome string, cause error) {
	attributes := map[string]string{"vm": vm.Name, "outcome": outcome, "error": cause.Error()}
	var failure *forwardMutationFailure
	if errors.As(cause, &failure) {
		attributes["operation"] = failure.request.Action()
		attributes["endpoint"] = forward.Address(failure.request.Endpoint.Bind, failure.request.Endpoint.HostPort)
		attributes["protocol"] = failure.request.Endpoint.Protocol
	} else if journal, journalErr := forward.ReadMutationJournal(m.store.Runtime(vm).ForwardMutation()); journalErr == nil && journal != nil {
		for _, request := range journal.Requests {
			if request.Result.Phase == forward.MutationUnknown || request.Error != "" {
				attributes["operation"] = request.Action()
				attributes["endpoint"] = forward.Address(request.Endpoint.Bind, request.Endpoint.HostPort)
				attributes["protocol"] = request.Endpoint.Protocol
				break
			}
		}
	}
	m.events.Emit(events.Event{Type: "forward", Action: "recovery", Actor: events.Actor{ID: vm.ID, Attributes: attributes}})
	m.appendAutoForwardLog(vm, "error", fmt.Sprintf("forward recovery %s: %v", outcome, cause))
}

func (m *Manager) emitVM(action string, vm *state.VMRecord, attributes map[string]string) {
	if attributes == nil {
		attributes = map[string]string{}
	}
	attributes["name"] = vm.Name
	m.events.Emit(events.Event{Type: "vm", Action: action, Actor: events.Actor{ID: vm.ID, Attributes: attributes}})
}

func (m *Manager) emitForward(action string, vm *state.VMRecord, row RuntimeAutoForward) {
	m.events.Emit(events.Event{
		Type: "forward", Action: action,
		Actor: events.Actor{ID: vm.ID, Attributes: map[string]string{
			"vm": vm.Name, "kind": "auto", "protocol": row.Protocol, "bind": row.Bind,
			"hostPort": strconv.Itoa(row.HostPort), "guestPort": strconv.Itoa(row.GuestPort),
			"guestTargetIP": row.GuestTargetIP, "reason": row.Reason,
		}},
	})
}
