//go:build ignore

// Modified by the Voom project; see https://github.com/mjrusso/voom.
package main

import (
	"net"
	"reflect"
	"testing"

	"github.com/containers/gvisor-tap-vsock/pkg/types"
)

func TestIsolateGuest(t *testing.T) {
	config := &GvproxyConfig{Stack: types.Configuration{
		NAT: map[string]string{
			"192.168.127.253": "192.0.2.1",
			"192.168.127.254": "127.0.0.1",
		},
		GatewayVirtualIPs: []string{"192.168.127.253", "192.168.127.254"},
		DNS: []types.Zone{{
			Name: "containers.internal.",
			Records: []types.Record{
				{Name: "gateway", IP: net.ParseIP("192.168.127.1")},
				{Name: "host", IP: net.ParseIP("192.168.127.254")},
			},
		}},
	}}
	isolateGuest(config)
	if !reflect.DeepEqual(config.Stack.NAT, map[string]string{"192.168.127.253": "192.0.2.1"}) {
		t.Fatalf("NAT = %#v", config.Stack.NAT)
	}
	if !reflect.DeepEqual(config.Stack.GatewayVirtualIPs, []string{"192.168.127.253"}) {
		t.Fatalf("gateway virtual IPs = %#v", config.Stack.GatewayVirtualIPs)
	}
	if records := config.Stack.DNS[0].Records; len(records) != 1 || records[0].Name != "gateway" {
		t.Fatalf("DNS records = %#v", records)
	}
}
