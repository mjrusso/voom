//go:build ignore

// Added by the Voom project; see https://github.com/mjrusso/voom.
package forwarder

import (
	"errors"
	"net"
	"testing"

	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"github.com/inetaf/tcpproxy"
)

type retryCloseListener struct {
	net.Listener
	fail bool
}

func (l *retryCloseListener) Close() error {
	if l.fail {
		l.fail = false
		return errors.New("close failed")
	}
	return l.Listener.Close()
}

type retryCloseUDP struct {
	udpConn
	fail bool
}

func (l *retryCloseUDP) Close() error {
	if l.fail {
		l.fail = false
		return errors.New("close failed")
	}
	return l.udpConn.Close()
}

func TestFailedCloseRemainsListedAndRetryable(t *testing.T) {
	for _, protocol := range []types.TransportProtocol{types.TCP, types.UDP} {
		t.Run(string(protocol), func(t *testing.T) {
			var entry proxy
			if protocol == types.TCP {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				failing := &retryCloseListener{Listener: listener, fail: true}
				p := &tcpproxy.Proxy{ListenFunc: func(string, string) (net.Listener, error) { return failing, nil }}
				p.AddRoute(listener.Addr().String(), tcpproxy.To("192.168.127.3:8080"))
				if err := p.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = p.Close(); _ = p.Wait() }()
				entry = proxy{Protocol: "tcp", Local: listener.Addr().String(), underlying: p}
			} else {
				listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				p, err := NewUDPProxy(&retryCloseUDP{udpConn: listener, fail: true}, nil)
				if err != nil {
					t.Fatal(err)
				}
				entry = proxy{Protocol: "udp", Local: listener.LocalAddr().String(), underlying: p}
			}
			f := &PortsForwarder{proxies: map[ProxyKey]proxy{key(protocol, entry.Local): entry}}
			defer f.Unexpose(protocol, entry.Local)
			if err := f.Unexpose(protocol, entry.Local); err == nil {
				t.Fatal("close failure reported success")
			}
			if len(f.proxies) != 1 {
				t.Fatal("failed close removed forwarding-table entry")
			}
			if err := f.Unexpose(protocol, entry.Local); err != nil {
				t.Fatal(err)
			}
			if len(f.proxies) != 0 {
				t.Fatal("successful retry retained forwarding-table entry")
			}
			if err := f.Unexpose(protocol, entry.Local); err != nil {
				t.Fatal(err)
			}
		})
	}
}
