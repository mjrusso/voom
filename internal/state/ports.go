package state

import "github.com/mjrusso/voom/internal/forward"

type portKey struct {
	protocol string
	port     int
}

// PortReservations is a snapshot of reserved host sockets.
type PortReservations struct {
	binds map[portKey][]string
}

// Reserved reports whether a host socket overlaps a reservation.
func (r PortReservations) Reserved(protocol, bind string, port int) bool {
	for _, reservedBind := range r.binds[portKey{protocol, port}] {
		if forward.BindsConflict(bind, reservedBind) {
			return true
		}
	}
	return false
}

// Reserve claims a socket in this snapshot if it does not overlap another reservation.
func (r PortReservations) Reserve(protocol, bind string, port int) bool {
	if r.Reserved(protocol, bind, port) {
		return false
	}
	r.add(protocol, bind, port)
	return true
}

func (r PortReservations) add(protocol, bind string, port int) {
	key := portKey{protocol, port}
	r.binds[key] = append(r.binds[key], bind)
}

// PortReservations reads one snapshot while the caller holds the global lock.
// Only the caller's automatic rows are excluded; unresolved mutations remain reserved.
func (s *Store) PortReservations(exceptAutoVMID string) (PortReservations, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vms, err := s.listVMsLocked()
	if err != nil {
		return PortReservations{}, err
	}
	reserved := PortReservations{binds: make(map[portKey][]string)}
	for _, vm := range vms {
		endpoints, err := s.VMForwardReservations(vm, vm.ID != exceptAutoVMID)
		if err != nil {
			return PortReservations{}, err
		}
		for _, endpoint := range endpoints {
			reserved.add(endpoint.Protocol, endpoint.Bind, endpoint.HostPort)
		}
	}
	return reserved, nil
}

// VMForwardReservations requires the global lock and a persisted VM record.
// Excluding automatic rows still retains unresolved mutation reservations.
func (s *Store) VMForwardReservations(vm *VMRecord, includeAuto bool) ([]forward.Decl, error) {
	reservations := []forward.Decl{{Protocol: "tcp", Bind: vm.Network.SSHBind, HostPort: vm.Network.SSHPort, GuestPort: 22}}
	reservations = append(reservations, vm.Network.Forwards...)
	mutations, err := forward.ReadMutationReservations(s.Runtime(vm).ForwardMutation())
	if err != nil {
		return nil, err
	}
	reservations = append(reservations, mutations...)
	if !includeAuto {
		return reservations, nil
	}
	rows, err := forward.ReadRuntimeState(s.Runtime(vm).AutoForwardsJSON())
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.Installed() {
			reservations = append(reservations, row.Endpoint())
		}
	}
	return reservations, nil
}

// PortReserved checks declared and runtime reservations within one transport.
func (s *Store) PortReserved(protocol, bind string, port int) (bool, error) {
	protocol, err := forward.NormalizeProtocol(protocol)
	if err != nil {
		return false, err
	}
	reserved, err := s.PortReservations("")
	if err != nil {
		return false, err
	}
	return reserved.Reserved(protocol, bind, port), nil
}
