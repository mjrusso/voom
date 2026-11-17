//go:build ignore

// Modified by the Voom project; see https://github.com/mjrusso/voom.
package virtualnetwork

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
)

func TestUDPHostForwardMultipleClients(t *testing.T) {
	host, err := New(&types.Configuration{MTU: 1500, Subnet: "192.168.127.0/24", GatewayIP: "192.168.127.1", GatewayMacAddress: "5a:94:ef:e4:0c:dd"})
	if err != nil {
		t.Fatal(err)
	}
	guest, err := New(&types.Configuration{MTU: 1500, Subnet: "192.168.127.0/24", GatewayIP: "192.168.127.3", GatewayMacAddress: "5a:94:ef:e4:0c:ee"})
	if err != nil {
		t.Fatal(err)
	}
	defer host.stack.Close()
	defer guest.stack.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go host.AcceptQemu(ctx, a)
	go guest.AcceptQemu(ctx, b)
	echo, err := gonet.DialUDP(guest.stack, &tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4([4]byte{192, 168, 127, 3}), Port: 5353}, nil, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		buf := make([]byte, 65507)
		for {
			n, from, err := echo.ReadFrom(buf)
			if err != nil {
				return
			}
			echo.WriteTo(buf[:n], from)
		}
	}()
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	local := probe.LocalAddr().String()
	probe.Close()
	mux := host.Mux()
	request := func(action string) {
		t.Helper()
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/services/forwarder/"+action, strings.NewReader(`{"protocol":"udp","local":"`+local+`","remote":"192.168.127.3:5353"}`)))
		if res.Code != 200 {
			t.Fatalf("%s: %d %s", action, res.Code, res.Body.String())
		}
	}
	request("expose")
	defer request("unexpose")
	clients := make([]net.Conn, 3)
	for i := range clients {
		clients[i], err = net.Dial("udp", local)
		if err != nil {
			t.Fatal(err)
		}
		defer clients[i].Close()
	}
	for round := 0; round < 3; round++ {
		for i, c := range clients {
			c.SetDeadline(time.Now().Add(3 * time.Second))
			if _, err := c.Write([]byte{byte(i), byte(round)}); err != nil {
				t.Fatal(err)
			}
		}
		for i, c := range clients {
			got := make([]byte, 100)
			n, err := c.Read(got)
			if err != nil || !bytes.Equal(got[:n], []byte{byte(i), byte(round)}) {
				t.Fatalf("client %d round %d: %x %v", i, round, got[:n], err)
			}
		}
	}
	c := clients[0]
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write(nil); err != nil {
		t.Fatal(err)
	}
	if n, err := c.Read(make([]byte, 1)); n != 0 || err != nil {
		t.Fatalf("empty datagram: %d %v", n, err)
	}
	request("unexpose")
	rebound, err := net.ListenPacket("udp", local)
	if err != nil {
		t.Fatal(err)
	}
	rebound.Close()
	request("expose")
	c.SetDeadline(time.Now().Add(3 * time.Second))
	c.Write([]byte("restart"))
	got := make([]byte, 100)
	n, err := c.Read(got)
	if err != nil || string(got[:n]) != "restart" {
		t.Fatalf("reinstall: %q %v", got[:n], err)
	}
}
