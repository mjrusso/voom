// Package gvproxy is a minimal HTTP client for the gvproxy control socket
// (port-forward registration and DHCP lease lookup).
package gvproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	clientsMu sync.Mutex
	clients   = map[string]*http.Client{}
)

// clientForSocket returns a cached http.Client that dials the given unix socket,
// so repeated calls to the same socket reuse connections.
func clientForSocket(sock string) *http.Client {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	if c, ok := clients[sock]; ok {
		return c
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}
	c := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	clients[sock] = c
	return c
}

// Expose registers a host-to-guest TCP forwarder with gvproxy.
func Expose(sock, local, remote string) error {
	return post(sock, "/services/forwarder/expose", map[string]string{"local": local, "remote": remote})
}

// Unexpose removes a previously registered host-to-guest TCP forwarder.
func Unexpose(sock, local string) error {
	return post(sock, "/services/forwarder/unexpose", map[string]string{"local": local})
}

// LookupLease returns the IP address gvproxy has leased to the given MAC, or an error if no lease matches.
func LookupLease(sock, mac string) (string, error) {
	leases := map[string]string{}
	if err := get(sock, "/services/dhcp/leases", &leases); err != nil {
		return "", err
	}
	mac = strings.ToLower(strings.TrimSpace(mac))
	for ip, leaseMAC := range leases {
		if strings.ToLower(strings.TrimSpace(leaseMAC)) == mac {
			return ip, nil
		}
	}
	return "", fmt.Errorf("no DHCP lease found for MAC %s", mac)
}

func post(sock, path string, body any) error {
	// json.Marshal on map[string]string / similar concrete shapes here cannot fail.
	b, _ := json.Marshal(body)
	resp, err := clientForSocket(sock).Post("http://unix"+path, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		rb, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gvproxy %s failed: %s", path, strings.TrimSpace(string(rb)))
	}
	return nil
}

func get(sock, path string, out any) error {
	resp, err := clientForSocket(sock).Get("http://unix" + path)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		rb, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gvproxy %s failed: %s", path, strings.TrimSpace(string(rb)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
