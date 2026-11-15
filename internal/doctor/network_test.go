package doctor

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

func TestDoctorChecksRunningNetworkPolicy(t *testing.T) {
	root, err := os.MkdirTemp("", "voom-doctor-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	for _, kind := range []string{"CONFIG", "STATE", "CACHE", "RUNTIME"} {
		t.Setenv("VOOM_"+kind+"_DIR", filepath.Join(root, kind))
	}
	st, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	record := &state.VMRecord{SchemaVersion: state.SchemaVersion, ID: "vm1", Name: "guest", Driver: "qemu"}
	if err := st.SaveVM(record); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(record.Name, record.ID); err != nil {
		t.Fatal(err)
	}
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "gvproxy")
	if err := state.CopyFile(context.Background(), shell, executable); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(executable, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-c", "while true; do sleep 1; done")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	rt := st.Runtime(record)
	if err := process.Record(rt.GVProxyProcessRecord(), cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetAllowedDestinations([]string{"100.64.0.10:443", "[fd7a:115c:a1e0::1]:443"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, policy, blockWarning, allowWarning string }{
		{"no policy endpoint", "", "cannot read active gvproxy network policy", ""},
		{"new binary without block flags", `{"blockedSubnets":[],"allowedDestinations":[]}`, "blocked subnets", "allowed destinations"},
		{"missing IPv6 block", `{"blockedSubnets":["100.64.0.0/10"],"allowedDestinations":[]}`, "blocked subnets", "allowed destinations"},
		{"current policy", `{"blockedSubnets":["100.64.0.0/10","fd7a:115c:a1e0::/48"],"allowedDestinations":["100.64.0.10:443","[fd7a:115c:a1e0::1]:443"]}`, "", ""},
		{"equivalent sets", `{"blockedSubnets":["fd7a:115c:a1e0::/48","100.64.0.1/10","100.64.0.0/10"],"allowedDestinations":["[fd7a:115c:a1e0::1]:443","[::ffff:100.64.0.10]:443","100.64.0.10:443"]}`, "", ""},
		{"stale allowlist", `{"blockedSubnets":["100.64.0.0/10","fd7a:115c:a1e0::/48"],"allowedDestinations":["100.64.0.11:443"]}`, "", "allowed destinations"},
		{"missing allowlist", `{"blockedSubnets":["100.64.0.0/10","fd7a:115c:a1e0::/48"],"allowedDestinations":[]}`, "", "allowed destinations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("unix", rt.NetworkSock())
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/services/gateway-forward/capabilities":
					_, _ = fmt.Fprintf(w, `{%q:1}`, gvproxy.BlockedSubnetsCapability)
				case "/services/network/policy":
					if tc.policy == "" {
						http.NotFound(w, r)
						return
					}
					_, _ = fmt.Fprint(w, tc.policy)
				default:
					http.NotFound(w, r)
				}
			})}
			go func() { _ = server.Serve(listener) }()
			defer func() { _ = server.Close() }()
			expected := map[string]string{
				"gvproxy-tailnet-block-guest":        tc.blockWarning,
				"gvproxy-allowed-destinations-guest": tc.allowWarning,
			}
			for _, check := range StateDiagnostics(st) {
				if message, ok := expected[check.Name]; ok {
					if check.Severity != "warn" || !strings.Contains(check.Message, "restart VM guest") || message == "" || !strings.Contains(check.Message, message) {
						t.Fatalf("diagnostic = %+v", check)
					}
					delete(expected, check.Name)
				}
			}
			for name, message := range expected {
				if message != "" {
					t.Fatalf("missing diagnostic %s: %s", name, message)
				}
			}
		})
	}
	t.Run("invalid config with multiple running helpers", func(t *testing.T) {
		second := &state.VMRecord{SchemaVersion: state.SchemaVersion, ID: "vm2", Name: "other", Driver: "qemu"}
		if err := st.SaveVM(second); err != nil {
			t.Fatal(err)
		}
		if err := st.RegisterVM(second.Name, second.ID); err != nil {
			t.Fatal(err)
		}
		if err := process.Record(st.Runtime(second).GVProxyProcessRecord(), cmd.Process.Pid); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(st.Paths().Config, "network.json")
		if err := os.WriteFile(path, []byte(`{"allowedDestinations":[""]}`), 0600); err != nil {
			t.Fatal(err)
		}
		for _, record := range []*state.VMRecord{record, second} {
			policy := `{"blockedSubnets":["100.64.0.0/10","fd7a:115c:a1e0::/48"],"allowedDestinations":[]}`
			if record.Name == "guest" {
				policy = `{"blockedSubnets":[],"allowedDestinations":[]}`
			}
			listener, err := net.Listen("unix", st.Runtime(record).NetworkSock())
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, policy)
			})}
			go func() { _ = server.Serve(listener) }()
			defer func() { _ = server.Close() }()
		}
		configErrors, blockWarnings := 0, 0
		for _, check := range StateDiagnostics(st) {
			switch check.Name {
			case "network-config":
				configErrors++
				if check.Severity != "fatal" || check.OK || !strings.Contains(check.Message, path) || !strings.Contains(check.Message, "allowedDestinations[0]") || !strings.Contains(check.Message, "fix network.json") || strings.Contains(check.Message, "restart VM") {
					t.Fatalf("invalid config diagnostic = %+v", check)
				}
			case "gvproxy-tailnet-block-guest":
				blockWarnings++
				if check.Severity != "warn" || !strings.Contains(check.Message, "blocked subnets") || !strings.Contains(check.Message, "fix network.json, then restart VM guest") {
					t.Fatalf("missing block diagnostic = %+v", check)
				}
			default:
				if strings.HasPrefix(check.Name, "gvproxy-") {
					t.Fatalf("unexpected policy diagnostic with invalid config: %+v", check)
				}
			}
		}
		if configErrors != 1 || blockWarnings != 1 {
			t.Fatalf("config errors=%d, missing block warnings=%d; want one of each", configErrors, blockWarnings)
		}
	})
}
