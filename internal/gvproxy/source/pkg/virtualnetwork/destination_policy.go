//go:build ignore

// Modified by the Voom project; see https://github.com/mjrusso/voom.
package virtualnetwork

import "github.com/containers/gvisor-tap-vsock/pkg/services/forwarder"

// DestinationPolicy reports the policy used by the guest TCP and UDP forwarders.
func (n *VirtualNetwork) DestinationPolicy() forwarder.DestinationPolicySnapshot {
	return n.destinationPolicy.Snapshot()
}
