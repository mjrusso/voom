package doctor

import (
	"context"
	"fmt"

	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
	"github.com/mjrusso/voom/internal/vm"
)

func gvproxyDiagnostics(st *state.Store, record *state.VMRecord, config *state.NetworkConfig) []Check {
	rt := st.Runtime(record)
	if _, ok := process.ValidRecord(rt.GVProxyProcessRecord(), "gvproxy"); !ok {
		return nil
	}
	action := "restart VM " + record.Name + " with Voom's bundled gvproxy to apply the current network policy"
	if config == nil {
		action = "fix network.json, then " + action
	}
	checks := []Check{}
	add := func(name string, err error) {
		if err != nil {
			checks = append(checks, Check{name + "-" + record.Name, "warn", false, err.Error() + "; " + action})
		}
	}
	policy, err := gvproxy.ProcessNetworkPolicy(context.Background(), rt.NetworkSock())
	if err != nil {
		add("gvproxy-tailnet-block", fmt.Errorf("cannot read active gvproxy network policy: %w", err))
		return checks
	}
	add("gvproxy-tailnet-block", vm.CheckTailnetBlock(policy))
	if config != nil {
		add("gvproxy-allowed-destinations", vm.CheckAllowedDestinations(policy, *config))
	}
	return checks
}
