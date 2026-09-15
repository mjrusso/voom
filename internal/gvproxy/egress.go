package gvproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"time"

	"github.com/mjrusso/voom/internal/host"
)

// GatewayCapability and GuestIsolationCapability identify gvproxy APIs required by Voom.
const (
	GatewayCapability        = "gateway-forward-v1"
	GuestIsolationCapability = "guest-isolation-v1"
)

const gatewayExposePath = "/services/gateway-forward/expose"

// GatewayRoute describes one private listener and its Unix backend.
type GatewayRoute struct {
	Local             string `json:"local"`
	Target            string `json:"target"`
	ActiveConnections int    `json:"activeConnections,omitempty"`
}

// ResolveCompatible returns the selected gvproxy executable after checking its required capabilities.
func ResolveCompatible(ctx context.Context, required ...string) (string, error) {
	executable, err := host.ExePath("gvproxy")
	if err != nil {
		return "", err
	}
	if err := CheckExecutable(ctx, executable, required...); err != nil {
		return "", err
	}
	return executable, nil
}

// CheckExecutable verifies capabilities reported by the selected gvproxy binary.
func CheckExecutable(ctx context.Context, executable string, required ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, executable, "-capabilities").Output()
	if err != nil {
		return fmt.Errorf("gvproxy capability probe failed: %w", err)
	}
	return checkCapabilities(data, required...)
}

func checkCapabilities(data []byte, required ...string) error {
	var caps map[string]int
	if err := json.Unmarshal(data, &caps); err != nil {
		return fmt.Errorf("invalid gvproxy capabilities: %w", err)
	}
	for _, capability := range required {
		if caps[capability] != 1 {
			return fmt.Errorf("gvproxy requires %s version 1; install Voom's pinned gvproxy build", capability)
		}
	}
	return nil
}

// CheckProcess verifies capabilities reported by a running gvproxy process.
func CheckProcess(ctx context.Context, socket string, required ...string) error {
	data, err := request(ctx, socket, http.MethodGet, "/services/gateway-forward/capabilities", nil)
	if err != nil {
		return err
	}
	return checkCapabilities(data, required...)
}

// GatewayRoutes observes private listeners and active connection counts.
func GatewayRoutes(ctx context.Context, socket string) ([]GatewayRoute, error) {
	data, err := request(ctx, socket, http.MethodGet, "/services/gateway-forward/all", nil)
	if err != nil {
		return nil, err
	}
	var routes []GatewayRoute
	err = json.Unmarshal(data, &routes)
	return routes, err
}

// GatewayRejected recognizes only responses guaranteed to precede mutation by gateway-forward-v1.
func GatewayRejected(err error) bool {
	var response *HTTPError
	return errors.As(err, &response) && response.Path == gatewayExposePath && (response.Status == 400 || response.Status == 408 || response.Status == 409)
}

// ExposeGateway installs a route; errors other than known rejections leave its outcome uncertain.
func ExposeGateway(ctx context.Context, socket string, route GatewayRoute) error {
	_, err := request(ctx, socket, http.MethodPost, gatewayExposePath, route)
	return err
}

// UnexposeGateway waits for listener and relay termination.
func UnexposeGateway(ctx context.Context, socket, local string) error {
	_, err := request(ctx, socket, http.MethodPost, "/services/gateway-forward/unexpose", GatewayRoute{Local: local})
	return err
}

// GatewayArgument encodes startup configuration as one shell-free argument.
func GatewayArgument(route GatewayRoute) string {
	data, _ := json.Marshal(route)
	return string(data)
}
