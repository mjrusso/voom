//go:build ignore

// Modified by the Voom project; see https://github.com/mjrusso/voom.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containers/gvisor-tap-vsock/pkg/services/forwarder"
	"github.com/containers/gvisor-tap-vsock/pkg/services/gatewayforward"
	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"github.com/containers/gvisor-tap-vsock/pkg/virtualnetwork"
)

func TestGatewayTwoProcesses(t *testing.T) {
	binary := os.Getenv("GVPROXY_TEST_BINARY")
	if binary == "" {
		t.Skip("set GVPROXY_TEST_BINARY to test two real gvproxy processes")
	}
	output, err := exec.Command(binary, "-capabilities").Output()
	if err != nil {
		t.Fatal(err)
	}
	var capabilities map[string]int
	if err := json.Unmarshal(output, &capabilities); err != nil {
		t.Fatal(err)
	}
	if capabilities["blocked-subnets-v1"] != 1 {
		t.Fatal("missing blocked-subnets-v1 capability")
	}
	if capabilities[gatewayforward.IsolationCapability] != 1 {
		t.Fatalf("missing %s capability", gatewayforward.IsolationCapability)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	type instance struct {
		client  *http.Client
		guest   *virtualnetwork.VirtualNetwork
		conn    net.Conn
		backend net.Conn
		server  net.Listener
		route   gatewayforward.Route
	}
	instances := []instance{}
	for i := 0; i < 2; i++ {
		dir, err := os.MkdirTemp("", "gw-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dir)
		target := filepath.Join(dir, "backend.sock")
		server, err := net.Listen("unix", target)
		if err != nil {
			t.Fatal(err)
		}
		defer server.Close()
		control, network := filepath.Join(dir, "control.sock"), filepath.Join(dir, "network.sock")
		route := gatewayforward.Route{Local: "192.168.127.1:3128", Target: target}
		raw, _ := json.Marshal(route)
		allowed, err := net.Listen("tcp4", "0.0.0.0:0")
		if err != nil {
			t.Fatal(err)
		}
		defer allowed.Close()
		denied, err := net.Listen("tcp4", "0.0.0.0:0")
		if err != nil {
			t.Fatal(err)
		}
		defer denied.Close()
		_, allowedPort, _ := net.SplitHostPort(allowed.Addr().String())
		_, deniedPort, _ := net.SplitHostPort(denied.Addr().String())
		configPath := filepath.Join(dir, "config.yaml")
		config := "stack:\n  nat: {192.168.127.253: 0.0.0.0}\n  gatewayVirtualIPs: [192.168.127.253]\n"
		if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, binary, "-config", configPath, "-listen", "unix://"+control, "-listen-qemu", "unix://"+network, "-gateway-forward", string(raw), "-block-subnet", "100.64.0.0/10", "-block-subnet", "fd7a:115c:a1e0::/48", "-block-subnet", "0.0.0.0/32", "-allow-destination", "0.0.0.0:"+allowedPort)
		var log bytes.Buffer
		cmd.Stdout, cmd.Stderr = &log, &log
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cmd.Process.Kill()
			cmd.Wait()
			if t.Failed() {
				t.Log(log.String())
			}
		})
		var wire net.Conn
		for ctx.Err() == nil {
			wire, err = net.Dial("unix", network)
			if err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		defer wire.Close()
		guest, err := virtualnetwork.New(&types.Configuration{MTU: 1500, Subnet: "192.168.127.0/24", GatewayIP: "192.168.127.2", GatewayMacAddress: "5a:94:ef:e4:0c:ee"})
		if err != nil {
			t.Fatal(err)
		}
		go guest.AcceptQemu(ctx, wire)
		allowedCtx, cancelAllowed := context.WithTimeout(ctx, 5*time.Second)
		allowedConn, err := guest.DialContextTCP(allowedCtx, net.JoinHostPort("192.168.127.253", allowedPort))
		cancelAllowed()
		if err != nil {
			t.Fatalf("allowed destination failed: %v", err)
		}
		allowedConn.Close()

		deniedCtx, cancelDenied := context.WithTimeout(ctx, 5*time.Second)
		blockedConn, blockedErr := guest.DialContextTCP(deniedCtx, net.JoinHostPort("192.168.127.253", deniedPort))
		cancelDenied()
		if blockedErr == nil {
			blockedConn.Close()
			t.Fatal("guest reached a blocked destination after NAT")
		}
		if netErr, ok := blockedErr.(net.Error); ok && netErr.Timeout() {
			t.Fatalf("blocked connection timed out instead of resetting: %v", blockedErr)
		}
		hostLoopback, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer hostLoopback.Close()
		_, port, err := net.SplitHostPort(hostLoopback.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		dialCtx, cancelDial := context.WithTimeout(ctx, 500*time.Millisecond)
		conn, err := guest.DialContextTCP(dialCtx, net.JoinHostPort("192.168.127.254", port))
		cancelDial()
		if err == nil {
			conn.Close()
			t.Fatal("guest reached host loopback")
		}
		guestHTTP := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) { return guest.DialContextTCP(ctx, addr) }}}
		victim := filepath.Join(dir, "victim")
		if err := os.WriteFile(victim, []byte("host data"), 0600); err != nil {
			t.Fatal(err)
		}
		res, err := guestHTTP.Post("http://192.168.127.1/services/forwarder/expose", "application/json", strings.NewReader(fmt.Sprintf(`{"local":%q,"remote":"tcp://192.168.127.3:1","protocol":"unix"}`, victim)))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if info, err := os.Stat(victim); err != nil {
			t.Fatalf("guest removed host file: %v", err)
		} else if info.Mode().Type() != 0 {
			t.Fatalf("guest replaced host file: mode=%v", info.Mode())
		}
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("guest forwarder API returned %d", res.StatusCode)
		}
		res, err = guestHTTP.Post("http://192.168.127.1/services/dns/add", "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("guest DNS mutation API returned %d", res.StatusCode)
		}
		client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", control)
		}}}
		capabilityResponse, err := client.Get("http://unix/services/gateway-forward/capabilities")
		if err != nil {
			t.Fatal(err)
		}
		var runningCapabilities map[string]int
		err = json.NewDecoder(capabilityResponse.Body).Decode(&runningCapabilities)
		capabilityResponse.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if capabilityResponse.StatusCode != http.StatusOK || !maps.Equal(capabilities, runningCapabilities) {
			t.Fatalf("CLI and control socket capabilities differ: CLI=%v HTTP=%d %v", capabilities, capabilityResponse.StatusCode, runningCapabilities)
		}
		policyResponse, err := client.Get("http://unix/services/network/policy")
		if err != nil {
			t.Fatal(err)
		}
		var activePolicy forwarder.DestinationPolicySnapshot
		err = json.NewDecoder(policyResponse.Body).Decode(&activePolicy)
		policyResponse.Body.Close()
		if err != nil || policyResponse.StatusCode != http.StatusOK {
			t.Fatalf("active policy: HTTP %d, %v", policyResponse.StatusCode, err)
		}
		if len(activePolicy.BlockedSubnets) != 3 || len(activePolicy.AllowedDestinations) != 1 || activePolicy.AllowedDestinations[0].String() != "0.0.0.0:"+allowedPort {
			t.Fatalf("wrong active policy: %+v", activePolicy)
		}
		res, err = guestHTTP.Get("http://192.168.127.1/services/network/policy")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("guest can read host network policy: HTTP %d", res.StatusCode)
		}
		guestConn, err := guest.DialContextTCP(ctx, route.Local)
		if err != nil {
			t.Fatal(err)
		}
		defer guestConn.Close()
		accepted := make(chan net.Conn, 1)
		go func() { c, _ := server.Accept(); accepted <- c }()
		var backend net.Conn
		select {
		case backend = <-accepted:
		case <-ctx.Done():
			t.Fatal("backend accept timed out")
		}
		if backend == nil {
			t.Fatal("backend unavailable")
		}
		defer backend.Close()
		marker := []byte{byte('A' + i)}
		guestConn.SetDeadline(time.Now().Add(15 * time.Second))
		backend.SetDeadline(time.Now().Add(15 * time.Second))
		guestConn.Write(marker)
		got := make([]byte, 1)
		if _, err := io.ReadFull(backend, got); err != nil || !bytes.Equal(got, marker) {
			t.Fatalf("wrong backend: %q %v", got, err)
		}
		instances = append(instances, instance{client, guest, guestConn, backend, server, route})
	}
	for _, in := range instances {
		gatewayRequest(t, in.client, "expose", in.route)
		guestHTTP := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) { return in.guest.DialContextTCP(ctx, addr) }}}
		res, err := guestHTTP.Get("http://192.168.127.1/services/gateway-forward/all")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 404 {
			t.Fatalf("guest API exposed: %d", res.StatusCode)
		}
	}
	a, b := instances[0], instances[1]
	gatewayRequest(t, a.client, "unexpose", gatewayforward.Route{Local: a.route.Local})
	for _, conn := range []net.Conn{a.conn, a.backend} {
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, err := conn.Read(make([]byte, 1))
		if err == nil {
			t.Fatal("relay survived removal")
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Fatal("relay did not terminate")
		}
	}
	b.backend.Write([]byte("B"))
	got := make([]byte, 1)
	if _, err := io.ReadFull(b.conn, got); err != nil || string(got) != "B" {
		t.Fatalf("other VM affected: %q %v", got, err)
	}
	if conn, err := a.guest.DialContextTCP(ctx, a.route.Local); err == nil {
		conn.Close()
		t.Fatal("disabled listener accepted connection")
	}
	gatewayRequest(t, a.client, "expose", a.route)
	conn, err := a.guest.DialContextTCP(ctx, a.route.Local)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func gatewayRequest(t *testing.T, client *http.Client, action string, route gatewayforward.Route) {
	t.Helper()
	raw, _ := json.Marshal(route)
	response, err := client.Post("http://unix/services/gateway-forward/"+action, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 204 {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("%s: %s", action, data)
	}
}

func TestInvalidDestinationPolicyFlags(t *testing.T) {
	binary := os.Getenv("GVPROXY_TEST_BINARY")
	if binary == "" {
		t.Skip("set GVPROXY_TEST_BINARY to test real gvproxy flag validation")
	}
	for _, tc := range []struct {
		args    []string
		message string
	}{
		{[]string{"-block-subnet", "100.64.0.0/33"}, "invalid blocked subnet"},
		{[]string{"-allow-destination", "host:443"}, "invalid allowed destination"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		output, err := exec.CommandContext(ctx, binary, append([]string{"-ssh-port", "-1"}, tc.args...)...).CombinedOutput()
		cancel()
		if err == nil || !strings.Contains(string(output), tc.message) {
			t.Fatalf("%q: %s, %v", tc.args, output, err)
		}
	}
}
