//go:build ignore

// Modified by the Voom project; see https://github.com/mjrusso/voom.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/containers/gvisor-tap-vsock/pkg/services/forwarder"
)

func TestNetworkPolicyWithoutFlags(t *testing.T) {
	binary := os.Getenv("GVPROXY_TEST_BINARY")
	if binary == "" {
		t.Skip("set GVPROXY_TEST_BINARY to test real gvproxy policy reporting")
	}
	directory, err := os.MkdirTemp("", "policy-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	control := filepath.Join(directory, "control.sock")
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	probe.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-ssh-port", "-1", "-listen", "unix://"+control, "-listen", "tcp://"+address)
	var log bytes.Buffer
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
		if t.Failed() {
			t.Log(log.String())
		}
	}()
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", control)
	}}}
	defer client.CloseIdleConnections()
	var response *http.Response
	for ctx.Err() == nil {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/services/network/policy", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err = client.Do(request)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if response == nil {
		t.Fatal("gvproxy control socket did not become ready")
	}
	var policy forwarder.DestinationPolicySnapshot
	err = json.NewDecoder(response.Body).Decode(&policy)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || len(policy.BlockedSubnets) != 0 || len(policy.AllowedDestinations) != 0 {
		t.Fatalf("unconfigured policy=%+v, HTTP %d, %v", policy, response.StatusCode, err)
	}
	response, err = client.Get("http://unix/services/gateway-forward/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	var capabilities map[string]int
	err = json.NewDecoder(response.Body).Decode(&capabilities)
	response.Body.Close()
	if err != nil || capabilities["blocked-subnets-v1"] != 1 {
		t.Fatalf("capabilities=%v, %v", capabilities, err)
	}
	hostClient := &http.Client{Timeout: 5 * time.Second}
	response, err = hostClient.Get("http://" + address + "/services/network/policy")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("policy exposed on TCP control listener: HTTP %d", response.StatusCode)
	}
}
