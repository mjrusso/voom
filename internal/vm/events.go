package vm

import (
	"strconv"

	"github.com/mjrusso/voom/internal/events"
	"github.com/mjrusso/voom/internal/state"
)

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
