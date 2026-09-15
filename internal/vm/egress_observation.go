package vm

import (
	"context"
	"errors"
	"os"

	"github.com/mjrusso/voom/internal/egress"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/state"
)

// EgressRuntime compares observed access and files with desired configuration.
type EgressRuntime struct {
	State             string `json:"state"`
	ActiveConnections *int   `json:"activeConnections,omitempty"`
	ManifestPresent   bool   `json:"manifestPresent"`
	CAPresent         bool   `json:"caPresent"`
	InSync            bool   `json:"inSync"`
	Error             string `json:"error,omitempty"`
}

// ObserveEgress compares CA copies with the current source without republishing.
func (m *Manager) ObserveEgress(ctx context.Context, vm *state.VMRecord) (EgressRuntime, error) {
	var ca []byte
	var sourceErr error
	if d := vm.Network.Egress; d.IsEnabled() {
		ca, sourceErr = egress.ReadCA(d.CACertPath)
	}
	result, err := m.observeEgress(ctx, vm, ca)
	if sourceErr != nil {
		err = errors.Join(sourceErr, err)
		result.InSync = false
		result.Error = err.Error()
	}
	return result, err
}

func (m *Manager) observeEgress(ctx context.Context, vm *state.VMRecord, ca []byte) (result EgressRuntime, err error) {
	result.State = "unknown"
	defer func() {
		if err != nil {
			result.Error = err.Error()
			result.InSync = false
		}
	}()
	rt := m.store.Runtime(vm)
	_, manifestErr := os.Lstat(rt.EgressManifest())
	result.ManifestPresent = manifestErr == nil
	_, caErr := os.Lstat(rt.EgressCA())
	result.CAPresent = caErr == nil
	routes, err := gvproxy.GatewayRoutes(ctx, rt.NetworkSock())
	if err != nil {
		return result, err
	}
	result.State = "inactive"
	var active *gvproxy.GatewayRoute
	for _, r := range routes {
		if r.Local == egress.Listener {
			active = &r
			result.State = "active"
			count := r.ActiveConnections
			result.ActiveConnections = &count
		}
	}
	enabled := vm.Network.Egress.IsEnabled()
	var manifest []byte
	if enabled {
		if err = egress.Syntax(*vm.Network.Egress); err != nil {
			return result, err
		}
		manifest = egress.ManifestBytes(ca)
	}
	manifestOK, err := egressFileMatches(rt.EgressManifest(), manifest)
	if err != nil {
		return result, err
	}
	caOK, err := egressFileMatches(rt.EgressCA(), ca)
	if err != nil {
		return result, err
	}
	routeOK := active == nil
	if enabled {
		routeOK = active != nil && active.Target == vm.Network.Egress.BackendSocket
	}
	result.InSync = routeOK && manifestOK && caOK
	if !result.InSync {
		return result, errors.New("private route or runtime files differ from desired egress configuration")
	}
	return result, nil
}
