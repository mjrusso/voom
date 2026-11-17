// Package gvproxy provides an HTTP client for gvproxy's Unix control socket.
package gvproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mjrusso/voom/internal/forward"
)

// ErrForwardOutcomeUnknown means the request's completion could not be confirmed.
var ErrForwardOutcomeUnknown = errors.New("gvproxy forward mutation outcome unknown")

// ErrForwardRequestRetired means the ID can no longer execute and its outcome is unavailable.
var ErrForwardRequestRetired = errors.New("gvproxy forward mutation ID retired")

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

// AllocateForwardIDs obtains ordered request IDs from the running gvproxy.
func AllocateForwardIDs(ctx context.Context, sock string, count int) ([]string, error) {
	if count <= 0 {
		return nil, errors.New("forwarding request count must be positive")
	}
	data, err := request(ctx, sock, http.MethodPost, "/services/forwarder/ids?count="+strconv.Itoa(count), nil)
	if err != nil {
		return nil, err
	}
	var allocation struct {
		First uint64 `json:"first"`
	}
	if err := json.Unmarshal(data, &allocation); err != nil {
		return nil, err
	}
	if allocation.First == 0 || uint64(count-1) > math.MaxUint64-allocation.First {
		return nil, errors.New("invalid forwarding request ID range")
	}
	ids := make([]string, count)
	for i := range ids {
		ids[i] = strconv.FormatUint(allocation.First+uint64(i), 10)
	}
	return ids, nil
}

// ChangeForward submits a mutation. Reusing its ID is safe only with the same
// action and endpoint, against a process with ForwardRecoveryCapability.
func ChangeForward(ctx context.Context, sock, action string, endpoint Forward, id string) (forward.MutationResult, error) {
	rejected := forward.RejectedMutation()
	if action != "expose" && action != "unexpose" {
		return rejected, errors.New("invalid forwarding operation")
	}
	protocol, err := forward.NormalizeProtocol(endpoint.Protocol)
	if err != nil {
		return rejected, err
	}
	endpoint.Protocol = protocol
	if protocol == "udp" {
		if err := CheckProcess(ctx, sock, UDPForwardCapability); err != nil {
			return rejected, fmt.Errorf("UDP forwarding unavailable: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return rejected, err
	}
	path := "/services/forwarder/" + action
	_, response, err := requestWithID(ctx, sock, http.MethodPost, path, endpoint, id)
	if response != nil {
		switch response.Header.Get("X-Voom-Forward-Outcome") {
		case "not-submitted":
			return rejected, err
		case "retired":
			return forward.RetiredMutation(), ErrForwardRequestRetired
		}
		status := response.StatusCode
		result := forward.CompletedMutation(action == "expose", status)
		if response.Header.Get("X-Voom-Forward-Outcome") == "completed" ||
			(status != http.StatusAccepted && (err == nil || result.Phase == forward.MutationCompletedRejected)) {
			if status >= 200 && status <= 599 {
				return result, err
			}
		}
	}
	return forward.UnknownMutation(), errors.Join(ErrForwardOutcomeUnknown, err)
}

// AcknowledgeForward frees the cached response body after the caller has
// durably recorded completion.
func AcknowledgeForward(ctx context.Context, sock, id string) error {
	_, err := request(ctx, sock, http.MethodPost, "/services/forwarder/acknowledge?id="+url.QueryEscape(id), nil)
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
	data, _, err := requestWithID(ctx, sock, method, path, body, "")
	return data, err
}

func requestWithID(ctx context.Context, sock, method, path string, body any, id string) ([]byte, *http.Response, error) {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if id != "" {
		req.Header.Set("X-Voom-Forward-ID", id)
	}
	res, err := clientForSocket(sock).Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = res.Body.Close() }()
	data, readErr := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return nil, res, &HTTPError{Path: path, Status: res.StatusCode, Body: string(bytes.TrimSpace(data))}
	}
	if readErr != nil {
		return nil, res, fmt.Errorf("gvproxy %s failed while reading response: %w", path, readErr)
	}
	return data, res, nil
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
