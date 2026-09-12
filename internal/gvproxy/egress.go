package gvproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"time"
)

// GatewayCapability identifies the complete private transport contract.
const GatewayCapability = "gateway-forward-v1"

// GatewayRoute describes one private listener and its Unix backend.
type GatewayRoute struct {
	Local             string `json:"local"`
	Target            string `json:"target"`
	ActiveConnections int    `json:"activeConnections,omitempty"`
}

// CheckGatewayExecutable probes the exact executable selected for startup.
func CheckGatewayExecutable(ctx context.Context, executable string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, executable, "-capabilities").Output()
	if err != nil {
		return fmt.Errorf("gvproxy private gateway capability unavailable: %w", err)
	}
	return checkGatewayCapabilities(data)
}

func checkGatewayCapabilities(data []byte) error {
	var caps map[string]int
	if err := json.Unmarshal(data, &caps); err != nil {
		return fmt.Errorf("invalid gvproxy capabilities: %w", err)
	}
	if caps[GatewayCapability] != 1 {
		return fmt.Errorf("gvproxy requires %s version 1; install Voom's pinned gvproxy build", GatewayCapability)
	}
	return nil
}

// CheckGatewayProcess queries the running process, independently of installed binaries.
func CheckGatewayProcess(ctx context.Context, socket string) error {
	data, err := gatewayRequest(ctx, socket, "GET", "capabilities", nil)
	if err != nil {
		return err
	}
	return checkGatewayCapabilities(data)
}

// GatewayRoutes observes private listeners and active connection counts.
func GatewayRoutes(ctx context.Context, socket string) ([]GatewayRoute, error) {
	data, err := gatewayRequest(ctx, socket, "GET", "all", nil)
	if err != nil {
		return nil, err
	}
	var routes []GatewayRoute
	err = json.Unmarshal(data, &routes)
	return routes, err
}

// GatewayHTTPError retains the response status even when its body cannot be read.
type GatewayHTTPError struct {
	Status         int
	Action, Detail string
}

func (e *GatewayHTTPError) Error() string {
	return fmt.Sprintf("gvproxy gateway %s: HTTP %d: %s", e.Action, e.Status, e.Detail)
}

// GatewayRejected recognizes only responses guaranteed to precede mutation by gateway-forward-v1.
func GatewayRejected(err error) bool {
	var response *GatewayHTTPError
	return errors.As(err, &response) && response.Action == "expose" && (response.Status == 400 || response.Status == 408 || response.Status == 409)
}

// ExposeGateway installs a route; errors other than known rejections leave its outcome uncertain.
func ExposeGateway(ctx context.Context, socket string, route GatewayRoute) error {
	_, err := gatewayRequest(ctx, socket, "POST", "expose", route)
	return err
}

// UnexposeGateway waits for listener and relay termination.
func UnexposeGateway(ctx context.Context, socket, local string) error {
	_, err := gatewayRequest(ctx, socket, "POST", "unexpose", GatewayRoute{Local: local})
	return err
}

// GatewayArgument encodes startup configuration as one shell-free argument.
func GatewayArgument(route GatewayRoute) string {
	data, _ := json.Marshal(route)
	return string(data)
}

func gatewayRequest(ctx context.Context, socket, method, action string, value any) ([]byte, error) {
	var body []byte
	if value != nil {
		body, _ = json.Marshal(value)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://unix/services/gateway-forward/"+action, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := clientForSocket(socket).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return nil, &GatewayHTTPError{Status: res.StatusCode, Action: action, Detail: string(bytes.TrimSpace(data))}
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}
