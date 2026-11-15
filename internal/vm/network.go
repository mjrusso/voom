package vm

import (
	"errors"
	"maps"
	"net/netip"

	"github.com/mjrusso/voom/internal/egress"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/state"
)

var tailnetSubnets = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

func gvproxyArguments(vm *state.VMRecord, rt state.RuntimeLayout, config state.NetworkConfig) []string {
	gvproxyArgs := []string{"-listen", "unix://" + rt.NetworkSock(), "-ssh-port", "-1"}
	for _, subnet := range tailnetSubnets {
		gvproxyArgs = append(gvproxyArgs, "-block-subnet", subnet.String())
	}
	for _, destination := range config.AllowedDestinations {
		gvproxyArgs = append(gvproxyArgs, "-allow-destination", destination.String())
	}
	switch vm.Driver {
	case "qemu":
		gvproxyArgs = append(gvproxyArgs, "-listen-qemu", "unix://"+rt.QEMUNetSock())
	case "vfkit":
		gvproxyArgs = append(gvproxyArgs, "-listen-vfkit", "unixgram://"+rt.VFKitNetSock())
	}
	if vm.Network.Egress.IsEnabled() {
		gvproxyArgs = append(gvproxyArgs, "-gateway-forward", gvproxy.GatewayArgument(gvproxy.GatewayRoute{Local: egress.Listener, Target: vm.Network.Egress.BackendSocket}))
	}
	return gvproxyArgs
}

// CheckTailnetBlock compares the reported subnets with Voom's required tailnet blocks.
func CheckTailnetBlock(policy gvproxy.NetworkPolicy) error {
	if !sameSet(policy.BlockedSubnets, tailnetSubnets) {
		return errors.New("running gvproxy's blocked subnets do not match Voom's tailnet policy")
	}
	return nil
}

// CheckAllowedDestinations compares the reported exceptions with the supplied settings.
func CheckAllowedDestinations(policy gvproxy.NetworkPolicy, config state.NetworkConfig) error {
	if !sameSet(policy.AllowedDestinations, config.AllowedDestinations) {
		return errors.New("running gvproxy's allowed destinations do not match network.json")
	}
	return nil
}

func sameSet[T comparable](a, b []T) bool {
	left, right := make(map[T]bool, len(a)), make(map[T]bool, len(b))
	for _, value := range a {
		left[value] = true
	}
	for _, value := range b {
		right[value] = true
	}
	return maps.Equal(left, right)
}
