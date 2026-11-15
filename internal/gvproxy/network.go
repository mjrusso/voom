package gvproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
)

// NetworkPolicy reports the destination policy installed in a running gvproxy.
type NetworkPolicy struct {
	BlockedSubnets      []netip.Prefix   `json:"blockedSubnets"`
	AllowedDestinations []netip.AddrPort `json:"allowedDestinations"`
}

// ProcessNetworkPolicy reads the policy through gvproxy's host Unix control socket.
func ProcessNetworkPolicy(ctx context.Context, socket string) (NetworkPolicy, error) {
	data, err := request(ctx, socket, http.MethodGet, "/services/network/policy", nil)
	if err != nil {
		return NetworkPolicy{}, err
	}
	var policy NetworkPolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		return NetworkPolicy{}, err
	}
	for i, subnet := range policy.BlockedSubnets {
		policy.BlockedSubnets[i] = subnet.Masked()
	}
	for i, destination := range policy.AllowedDestinations {
		policy.AllowedDestinations[i] = netip.AddrPortFrom(destination.Addr().Unmap(), destination.Port())
	}
	return policy, nil
}
