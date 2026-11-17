//go:build ignore

// Modified by the Voom project; see https://github.com/mjrusso/voom.
package main

import (
	"net"
	"slices"

	"github.com/containers/gvisor-tap-vsock/pkg/types"
)

func isolateGuest(config *GvproxyConfig) {
	blocked := make(map[string]struct{})
	for source, target := range config.Stack.NAT {
		if ip := net.ParseIP(target); ip != nil && ip.IsLoopback() {
			delete(config.Stack.NAT, source)
			blocked[source] = struct{}{}
		}
	}
	config.Stack.GatewayVirtualIPs = slices.DeleteFunc(config.Stack.GatewayVirtualIPs, func(ip string) bool {
		_, ok := blocked[ip]
		return ok
	})
	for i := range config.Stack.DNS {
		zone := &config.Stack.DNS[i]
		zone.Records = slices.DeleteFunc(zone.Records, func(record types.Record) bool {
			_, mappedToLoopback := blocked[record.IP.String()]
			return mappedToLoopback || ((zone.Name == "containers.internal." || zone.Name == "docker.internal.") && record.Name == host)
		})
	}
}
