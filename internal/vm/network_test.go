package vm

import (
	"context"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

func TestGVProxyArguments(t *testing.T) {
	store, _ := newTestStore(t)
	for _, driver := range []string{"qemu", "vfkit"} {
		for _, allowed := range [][]netip.AddrPort{nil, {netip.MustParseAddrPort("100.87.66.65:443"), netip.MustParseAddrPort("[fd7a:115c:a1e0::1]:443")}} {
			record := &state.VMRecord{ID: "vm1", Driver: driver}
			args := gvproxyArguments(record, store.Runtime(record), state.NetworkConfig{AllowedDestinations: allowed})
			for _, subnet := range []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"} {
				if !hasArgPair(args, "-block-subnet", subnet) {
					t.Fatalf("%s: missing block for %s in %q", driver, subnet, args)
				}
			}
			for _, destination := range allowed {
				if !hasArgPair(args, "-allow-destination", destination.String()) {
					t.Fatalf("%s: missing exception %s in %q", driver, destination, args)
				}
			}
			if len(allowed) == 0 && slices.Contains(args, "-allow-destination") {
				t.Fatalf("%s: unexpected exception in %q", driver, args)
			}
		}
	}
}

func TestStartRequiresBlockedSubnetsCapability(t *testing.T) {
	st, manager, record := newForwardLifecycleVM(t)
	executable := filepath.Join(t.TempDir(), "gvproxy")
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("#!"+sh+"\nprintf '%s\\n' '{\"guest-isolation-v1\":1}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VOOM_GVPROXY", executable)
	if _, err := manager.Start(context.Background(), io.Discard, record.Name); err == nil || !strings.Contains(err.Error(), gvproxy.BlockedSubnetsCapability) {
		t.Fatalf("start with old gvproxy = %v", err)
	}
	if process.HasRecord(st.Runtime(record).GVProxyProcessRecord()) {
		t.Fatal("started incompatible gvproxy")
	}
}

func TestStartRejectsInvalidNetworkConfigBeforeRuntimeCleanup(t *testing.T) {
	store, manager, record := newForwardLifecycleVM(t)
	rt := store.Runtime(record)
	helper := fakeNamedVMProcess(t, "gvproxy")
	writeProcessRecord(t, rt.GVProxyProcessRecord(), helper)
	configPath := filepath.Join(store.Paths().Config, "network.json")
	if err := os.WriteFile(configPath, []byte(`{"allowedDestinations":[""]}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := manager.Start(context.Background(), io.Discard, record.Name)
	if err == nil || !strings.Contains(err.Error(), configPath) {
		t.Fatalf("Start error=%v; want invalid network config", err)
	}
	if _, ok := process.ValidRecord(rt.GVProxyProcessRecord(), "gvproxy"); !ok {
		t.Fatal("invalid config caused runtime cleanup")
	}
}

func TestStoppedEgressRequiresMinimumCapabilities(t *testing.T) {
	store, _ := egressMutationStore(t)
	executable := os.Getenv("VOOM_GVPROXY")
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("#!"+shell+"\nprintf '%s\\n' '{\"guest-isolation-v1\":1,\"gateway-forward-v1\":1}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	record, err := store.LoadVM("a")
	if err != nil {
		t.Fatal(err)
	}
	if err := New(store).CheckEgressCapabilities(context.Background(), record); err == nil || !strings.Contains(err.Error(), gvproxy.BlockedSubnetsCapability) {
		t.Fatalf("stopped VM egress accepted incompatible gvproxy: %v", err)
	}
}
