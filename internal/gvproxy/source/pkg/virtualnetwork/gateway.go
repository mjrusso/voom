//go:build ignore

// Modified by the Voom project; see https://github.com/mjrusso/voom.
package virtualnetwork

import (
	"errors"
	"net"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"
)

func (n *VirtualNetwork) listenGateway(network, address string) (net.Listener, error) {
	ip, port, err := splitIPPort(network, address)
	if err != nil {
		return nil, err
	}
	var queue waiter.Queue
	ep, tcpErr := n.stack.NewEndpoint(tcp.ProtocolNumber, ipv4.ProtocolNumber, &queue)
	if tcpErr != nil {
		return nil, errors.New(tcpErr.String())
	}
	// Live re-enable must bind while connections from the previous attachment finish closing.
	ep.SocketOptions().SetReuseAddress(true)
	if tcpErr := ep.Bind(tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4Slice(ip.To4()), Port: port}); tcpErr != nil {
		ep.Close()
		return nil, errors.New(tcpErr.String())
	}
	if tcpErr := ep.Listen(128); tcpErr != nil {
		ep.Close()
		return nil, errors.New(tcpErr.String())
	}
	return gonet.NewTCPListener(n.stack, &queue, ep), nil
}
