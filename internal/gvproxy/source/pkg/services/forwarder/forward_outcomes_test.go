//go:build ignore

// Modified by the Voom project; see https://github.com/mjrusso/voom.
package forwarder

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/containers/gvisor-tap-vsock/pkg/types"
)

func TestExposeRejectionsPreserveListeners(t *testing.T) {
	for _, protocol := range []types.TransportProtocol{types.TCP, types.UDP} {
		t.Run(string(protocol), func(t *testing.T) {
			var occupied io.Closer
			var local string
			if protocol == types.TCP {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				occupied, local = listener, listener.Addr().String()
			} else {
				listener, err := net.ListenPacket("udp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				occupied, local = listener, listener.LocalAddr().String()
			}
			defer occupied.Close()
			f := &PortsForwarder{proxies: make(map[ProxyKey]proxy)}
			defer f.Unexpose(protocol, local)
			request := func(action string, want int) {
				t.Helper()
				body := fmt.Sprintf(`{"protocol":%q,"local":%q,"remote":"192.168.127.3:5353"}`, protocol, local)
				response := httptest.NewRecorder()
				f.Mux().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/"+action, strings.NewReader(body)))
				if response.Code != want {
					t.Fatalf("%s: %d, want %d: %s", action, response.Code, want, response.Body.String())
				}
			}
			request("expose", http.StatusConflict)
			if len(f.proxies) != 0 {
				t.Fatal("rejected expose installed a proxy")
			}
			if err := occupied.Close(); err != nil {
				t.Fatal(err)
			}
			request("expose", http.StatusOK)
			request("expose", http.StatusConflict)
			if len(f.proxies) != 1 {
				t.Fatal("duplicate rejection changed proxies")
			}
			request("unexpose", http.StatusOK)
			request("expose", http.StatusOK)
		})
	}
}

func TestUnexposeCloseFailureRemainsUnknown(t *testing.T) {
	local := "127.0.0.1:8080"
	f := &PortsForwarder{proxies: map[ProxyKey]proxy{
		key(types.TCP, local): {underlying: CloseWrapper(func() error { return errors.New("close failed") })},
	}}
	response := httptest.NewRecorder()
	f.Mux().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/unexpose", strings.NewReader(`{"protocol":"tcp","local":"127.0.0.1:8080"}`)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("close failure: %d", response.Code)
	}
}

type failingUDPListener struct {
	udpConn
	failure error
}

func (l failingUDPListener) Close() error {
	return errors.Join(l.udpConn.Close(), l.failure)
}

type failingUDPSession struct {
	net.Conn
	failure error
	closes  int
}

func (c *failingUDPSession) Close() error {
	c.closes++
	return errors.Join(c.Conn.Close(), c.failure)
}

func TestUDPUnexposeCloseFailuresRemainUnknown(t *testing.T) {
	for _, tc := range []struct {
		name              string
		listener, session bool
	}{
		{"listener", true, false},
		{"session", false, true},
		{"both", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
			if err != nil {
				t.Fatal(err)
			}
			var listenerErr, sessionErr error
			if tc.listener {
				listenerErr = errors.New("listener close failed")
			}
			if tc.session {
				sessionErr = errors.New("session close failed")
			}
			p, err := NewUDPProxy(failingUDPListener{listener, listenerErr}, nil)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				a, b := net.Pipe()
				defer b.Close()
				p.connTrackTable[connTrackKey{Port: i}] = &failingUDPSession{Conn: a, failure: sessionErr}
			}
			local := listener.LocalAddr().String()
			f := &PortsForwarder{proxies: map[ProxyKey]proxy{key(types.UDP, local): {underlying: p}}}
			response := httptest.NewRecorder()
			body := fmt.Sprintf(`{"protocol":"udp","local":%q}`, local)
			f.Mux().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/unexpose", strings.NewReader(body)))
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("close failure: %d %s", response.Code, response.Body.String())
			}
			for _, failure := range []error{listenerErr, sessionErr} {
				if failure != nil && !strings.Contains(response.Body.String(), failure.Error()) {
					t.Fatalf("lost close error %v: %s", failure, response.Body.String())
				}
			}
			for _, conn := range p.connTrackTable {
				if conn.(*failingUDPSession).closes != 1 {
					t.Fatal("cleanup did not attempt every session")
				}
			}
		})
	}
}
