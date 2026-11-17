//go:build ignore

// Modified by the Voom project; see https://github.com/mjrusso/voom.
package forwarder

import (
	"fmt"
	"net/netip"
	"slices"

	"gvisor.dev/gvisor/pkg/tcpip"
)

type DestinationPolicy struct {
	subnets []netip.Prefix
	allowed map[netip.AddrPort]struct{}
}

func NewDestinationPolicy(subnets, allowed []string) (*DestinationPolicy, error) {
	p := &DestinationPolicy{allowed: make(map[netip.AddrPort]struct{}, len(allowed))}
	for _, raw := range subnets {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid blocked subnet %q: %w", raw, err)
		}
		p.subnets = append(p.subnets, prefix.Masked())
	}
	for _, raw := range allowed {
		dest, err := netip.ParseAddrPort(raw)
		if err != nil || dest.Port() == 0 || dest.Addr().Zone() != "" {
			return nil, fmt.Errorf("invalid allowed destination %q: expected literal IP and port 1-65535", raw)
		}
		p.allowed[netip.AddrPortFrom(dest.Addr().Unmap(), dest.Port())] = struct{}{}
	}
	return p, nil
}

func (p *DestinationPolicy) Blocked(address tcpip.Address, port uint16) bool {
	ip, ok := netip.AddrFromSlice(address.AsSlice())
	if !ok {
		return true
	}
	ip = ip.Unmap()
	if ip.IsLoopback() {
		return true
	}
	if _, ok := p.allowed[netip.AddrPortFrom(ip, port)]; ok {
		return false
	}
	for _, subnet := range p.subnets {
		if subnet.Contains(ip) {
			return true
		}
	}
	return false
}

// DestinationPolicySnapshot describes the installed subnet blocks and destination exceptions.
type DestinationPolicySnapshot struct {
	BlockedSubnets      []netip.Prefix   `json:"blockedSubnets"`
	AllowedDestinations []netip.AddrPort `json:"allowedDestinations"`
}

// Snapshot returns a copy of the policy used for outbound connections.
func (p *DestinationPolicy) Snapshot() DestinationPolicySnapshot {
	allowed := make([]netip.AddrPort, 0, len(p.allowed))
	for destination := range p.allowed {
		allowed = append(allowed, destination)
	}
	slices.SortFunc(allowed, netip.AddrPort.Compare)
	return DestinationPolicySnapshot{BlockedSubnets: append([]netip.Prefix{}, p.subnets...), AllowedDestinations: allowed}
}
