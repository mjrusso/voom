//go:build ignore

// Modified by the Voom project; see https://github.com/mjrusso/voom.
package main

import (
	"flag"
	"testing"

	"github.com/stretchr/testify/assert"
	yaml "gopkg.in/yaml.v3"
)

func TestDestinationPolicyConfiguration(t *testing.T) {
	var config GvproxyConfig
	if err := yaml.Unmarshal([]byte("stack:\n  blockedSubnets: [100.64.0.0/10]\n  allowedDestinations: ['100.87.66.65:443']\n"), &config); err != nil {
		t.Fatal(err)
	}
	var args GvproxyArgs
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	if _, err := GvproxyArgParse(flags, &args, []string{"-ssh-port", "-1", "-block-subnet", "fd7a:115c:a1e0::/48", "-block-subnet", "192.0.2.0/24", "-allow-destination", "[fd7a:115c:a1e0::1]:443", "-allow-destination", "192.0.2.1:80"}); err != nil {
		t.Fatal(err)
	}
	if _, err := GvproxyConfigure(&config, &args, "test"); err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48", "192.0.2.0/24"}, config.Stack.BlockedSubnets)
	assert.Equal(t, []string{"100.87.66.65:443", "[fd7a:115c:a1e0::1]:443", "192.0.2.1:80"}, config.Stack.AllowedDestinations)
}
