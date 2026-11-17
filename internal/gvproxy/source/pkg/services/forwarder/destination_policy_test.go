//go:build ignore

// Modified by the Voom project; see https://github.com/mjrusso/voom.
package forwarder

import (
	"context"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/containers/gvisor-tap-vsock/pkg/tap"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

func TestDestinationPolicy(t *testing.T) {
	p, err := NewDestinationPolicy([]string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}, []string{"100.87.66.65:443", "[fd7a:115c:a1e0::1]:443", "127.0.0.1:443", "[::1]:443"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		dest    string
		blocked bool
	}{
		{"100.64.0.1:80", true}, {"100.127.255.255:443", true}, {"100.100.100.100:53", true},
		{"100.87.66.65:443", false}, {"100.87.66.65:22", true}, {"100.87.66.66:443", true},
		{"100.63.255.255:80", false}, {"100.128.0.0:443", false}, {"1.1.1.1:443", false},
		{"[fd7a:115c:a1e0::2]:443", true}, {"[fd7a:115c:a1e0::1]:443", false},
		{"[fd7a:115c:a1e0::1]:22", true}, {"[::ffff:100.64.0.1]:443", true},
		{"127.0.0.1:443", true}, {"[::1]:443", true}, {"[::ffff:127.0.0.1]:443", true},
	} {
		dest := netip.MustParseAddrPort(tc.dest)
		if got := p.Blocked(tcpip.AddrFromSlice(dest.Addr().AsSlice()), dest.Port()); got != tc.blocked {
			t.Errorf("%s blocked = %v", tc.dest, got)
		}
	}
	for _, subnet := range []string{"tailnet", "100.64.0.0/33", "fd7a::/129"} {
		if _, err := NewDestinationPolicy([]string{subnet}, nil); err == nil {
			t.Errorf("accepted subnet %q", subnet)
		}
	}
	for _, dest := range []string{"host:443", "100.64.0.1:0", "100.64.0.1:65536", "100.64.0.1:https", "[fe80::1%lo]:443"} {
		if _, err := NewDestinationPolicy(nil, []string{dest}); err == nil {
			t.Errorf("accepted destination %q", dest)
		}
	}
}

type forwarderTestLink struct {
	*tap.LinkEndpoint
	dispatcher stack.NetworkDispatcher
	packets    chan *stack.PacketBuffer
}

func (e *forwarderTestLink) Attach(d stack.NetworkDispatcher)             { e.dispatcher = d }
func (e *forwarderTestLink) IsAttached() bool                             { return e.dispatcher != nil }
func (e *forwarderTestLink) Capabilities() stack.LinkEndpointCapabilities { return 0 }
func (e *forwarderTestLink) WritePackets(packets stack.PacketBufferList) (int, tcpip.Error) {
	for _, p := range packets.AsSlice() {
		e.packets <- stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithView(p.ToView())})
	}
	return packets.Len(), nil
}

func forwarderStacks(t *testing.T, subnets, allowed []string, nat map[tcpip.Address]tcpip.Address) *stack.Stack {
	t.Helper()
	policy, err := NewDestinationPolicy(subnets, allowed)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	endpoints := make([]*forwarderTestLink, 2)
	for i := range endpoints {
		ep, err := tap.NewLinkEndpoint(false, 1500, "5a:94:ef:e4:0c:dd", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		endpoints[i] = &forwarderTestLink{LinkEndpoint: ep, packets: make(chan *stack.PacketBuffer, 256)}
	}
	stacks := make([]*stack.Stack, 2)
	for i, ep := range endpoints {
		s := stack.New(stack.Options{NetworkProtocols: []stack.NetworkProtocolFactory{ipv4.NewProtocol}, TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol}})
		t.Cleanup(s.Close)
		if err := s.CreateNIC(1, ep); err != nil {
			t.Fatal(err)
		}
		if err := s.AddProtocolAddress(1, tcpip.ProtocolAddress{Protocol: ipv4.ProtocolNumber, AddressWithPrefix: tcpip.AddrFrom4([4]byte{192, 0, 2, byte(i + 1)}).WithPrefix()}, stack.AddressProperties{}); err != nil {
			t.Fatal(err)
		}
		s.SetPromiscuousMode(1, true)
		s.SetSpoofing(1, true)
		s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: 1}})
		stacks[i] = s
	}
	for i, ep := range endpoints {
		peer := endpoints[1-i]
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case packet := <-ep.packets:
					peer.dispatcher.DeliverNetworkPacket(ipv4.ProtocolNumber, packet)
					packet.DecRef()
				}
			}
		}()
	}
	var lock sync.Mutex
	stacks[0].SetTransportProtocolHandler(tcp.ProtocolNumber, TCP(stacks[0], nat, &lock, false, policy).HandlePacket)
	stacks[0].SetTransportProtocolHandler(udp.ProtocolNumber, UDP(stacks[0], nat, &lock, false, policy).HandlePacket)
	return stacks[1]
}

func TestForwarderDestinationBlock(t *testing.T) {
	const timeout = 5 * time.Second

	for _, protocol := range []string{"tcp", "udp"} {
		// NAT to 0.0.0.0 reaches the host listener in Nix's loopback-only sandbox.
		for _, tc := range []struct {
			name, translated, subnet string
			allow, blocked           bool
		}{
			{"blocked", "", "100.64.0.0/10", false, true},
			{"allowed", "0.0.0.0", "0.0.0.0/32", true, false},
			{"ordinary", "0.0.0.0", "100.64.0.0/10", false, false},
			{"blocked-after-NAT", "0.0.0.0", "0.0.0.0/32", false, true},
			{"loopback-exception", "127.0.0.1", "100.64.0.0/10", true, true},
		} {
			t.Run(protocol+"/"+tc.name, func(t *testing.T) {
				var port uint16
				received := make(chan struct{}, 1)
				if protocol == "tcp" {
					ln, err := net.Listen("tcp4", "0.0.0.0:0")
					if err != nil {
						t.Fatal(err)
					}
					defer ln.Close()
					port = uint16(ln.Addr().(*net.TCPAddr).Port)
					go func() {
						c, err := ln.Accept()
						if err != nil {
							return
						}
						defer c.Close()
						c.SetDeadline(time.Now().Add(timeout))
						b := make([]byte, 1)
						if _, err := io.ReadFull(c, b); err == nil {
							received <- struct{}{}
							c.Write(b)
						}
					}()
				} else {
					pc, err := net.ListenPacket("udp4", "0.0.0.0:0")
					if err != nil {
						t.Fatal(err)
					}
					defer pc.Close()
					port = uint16(pc.LocalAddr().(*net.UDPAddr).Port)
					go func() {
						b := make([]byte, 1)
						n, from, err := pc.ReadFrom(b)
						if err == nil {
							received <- struct{}{}
							pc.WriteTo(b[:n], from)
						}
					}()
				}
				target := tcpip.AddrFrom4([4]byte{100, 64, 0, 10})
				var allowed []string
				if tc.allow {
					allowed = []string{net.JoinHostPort(tc.translated, strconv.Itoa(int(port)))}
				}
				nat := make(map[tcpip.Address]tcpip.Address)
				if tc.translated != "" {
					nat[target] = tcpip.AddrFromSlice(net.ParseIP(tc.translated).To4())
				}
				guest := forwarderStacks(t, []string{tc.subnet}, allowed, nat)
				dest := tcpip.FullAddress{NIC: 1, Addr: target, Port: port}
				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				defer cancel()
				var conn net.Conn
				var err error
				if protocol == "tcp" {
					conn, err = gonet.DialContextTCP(ctx, guest, dest, ipv4.ProtocolNumber)
				} else {
					c, e := gonet.DialUDP(guest, nil, &dest, ipv4.ProtocolNumber)
					if e != nil {
						t.Fatal(e)
					}
					conn = c
				}
				if protocol == "tcp" && tc.blocked {
					if err == nil {
						conn.Close()
						t.Fatal("blocked TCP connection succeeded")
					}
					if ctx.Err() != nil {
						t.Fatalf("blocked TCP connection timed out instead of resetting: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				deadline := time.Now().Add(timeout)
				if tc.blocked {
					deadline = time.Now().Add(300 * time.Millisecond)
				}
				conn.SetDeadline(deadline)
				if _, err := conn.Write([]byte("x")); err != nil {
					t.Fatal(err)
				}
				b := make([]byte, 1)
				_, err = conn.Read(b)
				if tc.blocked {
					if err == nil {
						t.Fatal("blocked UDP flow received a reply")
					}
					select {
					case <-received:
						t.Fatal("blocked UDP flow reached host")
					default:
					}
				} else if err != nil || string(b) != "x" {
					t.Fatalf("forwarded reply = %q, %v", b, err)
				}
			})
		}
	}
}
