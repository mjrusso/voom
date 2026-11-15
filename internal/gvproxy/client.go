// Package gvproxy provides an HTTP client for gvproxy's Unix control socket.
package gvproxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mjrusso/voom/internal/forward"
)

// ErrForwardOutcomeUnknown means a mutation may have completed despite its error.
var ErrForwardOutcomeUnknown = errors.New("gvproxy forward mutation outcome unknown")

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

// Expose registers a host-to-guest TCP or UDP forwarder with gvproxy.
func Expose(sock, protocol, local, remote string) error {
	protocol, err := forward.NormalizeProtocol(protocol)
	if err != nil {
		return err
	}
	if protocol == "udp" {
		if err := CheckProcess(context.Background(), sock, UDPForwardCapability); err != nil {
			return fmt.Errorf("UDP forwarding unavailable: %w", err)
		}
	}
	return mutateForward(sock, "expose", Forward{Protocol: protocol, Local: local, Remote: remote})
}

// Unexpose removes a previously registered host-to-guest TCP or UDP forwarder.
func Unexpose(sock, protocol, local string) error {
	protocol, err := forward.NormalizeProtocol(protocol)
	if err != nil {
		return err
	}
	if protocol == "udp" {
		if err := CheckProcess(context.Background(), sock, UDPForwardCapability); err != nil {
			return fmt.Errorf("UDP forwarding unavailable: %w", err)
		}
	}
	return mutateForward(sock, "unexpose", Forward{Protocol: protocol, Local: local})
}

func mutateForward(sock, action string, endpoint Forward) error {
	id := rand.Text()
	path := "/services/forwarder/" + action
	_, err := requestWithID(context.Background(), sock, http.MethodPost, path, endpoint, id)
	err = forwardMutationError(err)
	if err == nil {
		return nil
	}
	// Cancellation does not fence the server. Query the original handler's
	// outcome before trusting /all; a snapshot alone can precede a late expose.
	for attempt := 0; errors.Is(err, ErrForwardOutcomeUnknown) && attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(100 * time.Millisecond)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		data, queryErr := request(ctx, sock, http.MethodGet, "/services/forwarder/outcome?id="+id, nil)
		cancel()
		if queryErr != nil {
			continue
		}
		var outcome struct {
			Status int    `json:"status"`
			Body   string `json:"body"`
		}
		if json.Unmarshal(data, &outcome) != nil || outcome.Status == 0 {
			continue
		}
		if outcome.Status >= 200 && outcome.Status < 300 {
			if verifyErr := forwardMatches(sock, action, endpoint); verifyErr != nil {
				return errors.Join(err, verifyErr)
			}
			return nil
		}
		err = forwardMutationError(&HTTPError{Path: path, Status: outcome.Status, Body: strings.TrimSpace(outcome.Body)})
		break
	}
	var response *HTTPError
	if action == "expose" && errors.As(err, &response) && response.Status == http.StatusConflict {
		// A conflict can also mean a failed bind; only an exact target match
		// establishes that exposure is already satisfied.
		if forwardMatches(sock, action, endpoint) == nil {
			return nil
		}
	}
	return err
}

func forwardMatches(sock, action string, endpoint Forward) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	installed, err := Forwards(ctx, sock)
	if err != nil {
		return err
	}
	if action == "expose" {
		if _, found := installed[endpoint]; found {
			return nil
		}
	} else {
		for row := range installed {
			if row.Protocol == endpoint.Protocol && row.Local == endpoint.Local {
				return errors.New("forward is still installed")
			}
		}
		return nil
	}
	return errors.New("expected forward is absent from gvproxy")
}

func forwardMutationError(err error) error {
	if err == nil {
		return nil
	}
	var response *HTTPError
	if errors.As(err, &response) {
		// HTTP 409 from expose guarantees that no listener was installed.
		if response.Status == http.StatusBadRequest ||
			(response.Path == "/services/forwarder/expose" && response.Status == http.StatusConflict) {
			return err
		}
	}
	return errors.Join(ErrForwardOutcomeUnknown, err)
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
	return requestWithID(ctx, sock, method, path, body, "")
}

func requestWithID(ctx context.Context, sock, method, path string, body any, id string) ([]byte, error) {
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
	if id != "" {
		req.Header.Set("X-Voom-Forward-ID", id)
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

// Forward is a host listener and guest target reported by gvproxy.
type Forward struct {
	Protocol string `json:"protocol"`
	Local    string `json:"local"`
	Remote   string `json:"remote"`
}

// Forwards indexes the installed host-to-guest forwards.
func Forwards(ctx context.Context, sock string) (map[Forward]struct{}, error) {
	data, err := request(ctx, sock, http.MethodGet, "/services/forwarder/all", nil)
	if err != nil {
		return nil, err
	}
	var rows []Forward
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, err
	}
	installed := make(map[Forward]struct{}, len(rows))
	for _, row := range rows {
		installed[row] = struct{}{}
	}
	return installed, nil
}
