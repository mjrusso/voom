// Package gvproxy provides an HTTP client for gvproxy's Unix control socket.
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
	_, err := request(context.Background(), sock, http.MethodPost, "/services/forwarder/expose", map[string]string{"local": local, "remote": remote})
	return err
}

// Unexpose removes a previously registered host-to-guest TCP forwarder.
func Unexpose(sock, local string) error {
	_, err := request(context.Background(), sock, http.MethodPost, "/services/forwarder/unexpose", map[string]string{"local": local})
	return err
}

// LookupLease returns the IP address gvproxy has leased to the given MAC, or an error if no lease matches.
func LookupLease(sock, mac string) (string, error) {
	leases := map[string]string{}
	data, err := request(context.Background(), sock, http.MethodGet, "/services/dhcp/leases", nil)
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(data, &leases); err != nil {
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

// HTTPError records a non-successful gvproxy response.
type HTTPError struct {
	Path   string
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("gvproxy %s failed: HTTP %d", e.Path, e.Status)
	}
	return fmt.Sprintf("gvproxy %s failed: HTTP %d: %s", e.Path, e.Status, e.Body)
}

func request(ctx context.Context, sock, method, path string, body any) ([]byte, error) {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := clientForSocket(sock).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	data, readErr := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return nil, &HTTPError{Path: path, Status: res.StatusCode, Body: string(bytes.TrimSpace(data))}
	}
	if readErr != nil {
		return nil, fmt.Errorf("gvproxy %s failed while reading response: %w", path, readErr)
	}
	return data, nil
}
