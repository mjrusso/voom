package cli

import (
	"bytes"
	"encoding/json"
	"net/netip"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mjrusso/voom/internal/state"
)

func TestAllowedDestinationsCommands(t *testing.T) {
	root := t.TempDir()
	for _, kind := range []string{"CONFIG", "STATE", "CACHE", "RUNTIME"} {
		t.Setenv("VOOM_"+kind+"_DIR", filepath.Join(root, kind))
	}
	run := func(args ...string) (state.NetworkConfig, error) {
		t.Helper()
		cmd := NewRootCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"--output", "json", "config", "allowed-destinations"}, args...))
		var config state.NetworkConfig
		err := cmd.Execute()
		if err == nil {
			err = json.Unmarshal(out.Bytes(), &config)
		}
		return config, err
	}
	want := []netip.AddrPort{netip.MustParseAddrPort("100.64.0.10:443"), netip.MustParseAddrPort("[fd7a:115c:a1e0::1]:443")}
	if config, err := run("set", "100.64.0.10:443", "[fd7a:115c:a1e0::1]:443"); err != nil || !reflect.DeepEqual(config.AllowedDestinations, want) {
		t.Fatalf("set = %+v, %v", config, err)
	}
	if _, err := run("set", "hostname:443"); err == nil {
		t.Fatal("set accepted hostname")
	}
	if _, err := run("set"); err == nil {
		t.Fatal("set accepted no destinations")
	}
	if config, err := run(); err != nil || !reflect.DeepEqual(config.AllowedDestinations, want) {
		t.Fatalf("show = %+v, %v", config, err)
	}
	if config, err := run("clear"); err != nil || len(config.AllowedDestinations) != 0 {
		t.Fatalf("clear = %+v, %v", config, err)
	}
	if config, err := run(); err != nil || len(config.AllowedDestinations) != 0 {
		t.Fatalf("show after clear = %+v, %v", config, err)
	}
}
