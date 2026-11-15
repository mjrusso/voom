package state

import (
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSetAllowedDestinationsValidation(t *testing.T) {
	store, _ := newTestStore(t)
	for _, tc := range []struct {
		input   []string
		want    []string
		invalid bool
	}{
		{nil, []string{}, false},
		{[]string{"100.87.66.65:443", "[fd7a:115c:a1e0::1]:65535", "100.87.66.65:443"}, []string{"100.87.66.65:443", "[fd7a:115c:a1e0::1]:65535"}, false},
		{[]string{"[::ffff:100.87.66.65]:443"}, []string{"100.87.66.65:443"}, false},
		{[]string{"example.com:443"}, nil, true},
		{[]string{"100.87.66.65:0"}, nil, true},
		{[]string{"100.87.66.65:65536"}, nil, true},
		{[]string{"100.87.66.65:https"}, nil, true},
		{[]string{"100.87.66.65"}, nil, true},
		{[]string{"fd7a:115c:a1e0::1:443"}, nil, true},
		{[]string{"[fe80::1%eth0]:443"}, nil, true},
	} {
		config, err := store.SetAllowedDestinations(tc.input)
		var want []netip.AddrPort
		if tc.want != nil {
			want = make([]netip.AddrPort, 0, len(tc.want))
			for _, raw := range tc.want {
				want = append(want, netip.MustParseAddrPort(raw))
			}
		}
		if (err != nil) != tc.invalid || !reflect.DeepEqual(config.AllowedDestinations, want) {
			t.Errorf("%q = %v, %v; want %v, invalid %v", tc.input, config.AllowedDestinations, err, want, tc.invalid)
		}
		if err == nil {
			var stored struct {
				AllowedDestinations []string `json:"allowedDestinations"`
			}
			if err := ReadJSON(store.networkConfigPath(), &stored); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(stored.AllowedDestinations, tc.want) {
				t.Errorf("stored JSON destinations = %q; want %q", stored.AllowedDestinations, tc.want)
			}
		}
	}
}

func TestNetworkConfig(t *testing.T) {
	st, _ := newTestStore(t)
	config, err := st.LoadNetworkConfig()
	if err != nil || len(config.AllowedDestinations) != 0 {
		t.Fatalf("missing config = %+v, %v", config, err)
	}
	path := st.networkConfigPath()
	for _, data := range []string{
		`{`,
		`{"allowedDestinations":["host:443"]}`,
		`{"allowedDestinations":["100.64.0.10:443",42]}`,
		`{"allowedDestinations":[""]}`,
		`{"allowedDestinations":[null]}`,
		`{"allowedDestinations":["100.64.0.10:0"]}`,
		`{"allowedDestinations":["[fe80::1%eth0]:443"]}`,
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		config, err := st.LoadNetworkConfig()
		if err == nil || !reflect.DeepEqual(config, NetworkConfig{}) {
			t.Fatalf("invalid config %s returned %+v, %v", data, config, err)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "managed.json")
	if err := os.WriteFile(source, []byte(`{"allowedDestinations":["100.87.66.65:443"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, path); err != nil {
		t.Fatal(err)
	}
	if config, err := st.SetAllowedDestinations(nil); err == nil || !reflect.DeepEqual(config, NetworkConfig{}) {
		t.Fatalf("managed symlink update returned %+v, %v", config, err)
	}
	config, err = st.LoadNetworkConfig()
	if err != nil || !reflect.DeepEqual(config.AllowedDestinations, []netip.AddrPort{netip.MustParseAddrPort("100.87.66.65:443")}) {
		t.Fatalf("managed config = %+v, %v", config, err)
	}
}

func TestSetAllowedDestinationsReturnsZeroOnWriteError(t *testing.T) {
	store, _ := newTestStore(t)
	if err := os.Mkdir(store.networkConfigPath(), 0700); err != nil {
		t.Fatal(err)
	}
	if config, err := store.SetAllowedDestinations([]string{"100.64.0.10:443"}); err == nil || !reflect.DeepEqual(config, NetworkConfig{}) {
		t.Fatalf("failed write returned %+v, %v", config, err)
	}
}

func TestNetworkConfigErrorsNameFileAndEntry(t *testing.T) {
	store, _ := newTestStore(t)
	for _, tc := range []struct{ data, message string }{
		{`{"allowedDestinations":[""]}`, "allowedDestinations[0]: destination is empty or null"},
		{`{"allowedDestinations":[null]}`, "allowedDestinations[0]: destination is empty or null"},
		{`{"allowedDestinations":["100.64.0.10:443","100.64.0.10:0"]}`, "allowedDestinations[1]: port must be between 1 and 65535"},
		{`{"allowedDestinations":["[fe80::1%eth0]:443"]}`, "allowedDestinations[0]: IPv6 zone identifiers are not allowed"},
	} {
		path := store.networkConfigPath()
		if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := store.LoadNetworkConfig()
		if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), tc.message) {
			t.Fatalf("%s: error=%v; want file path and %q", tc.data, err, tc.message)
		}
	}
}
