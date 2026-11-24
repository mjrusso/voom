package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

func TestForwardingWarningsBoundSlowProcessInspection(t *testing.T) {
	st, _ := newTestStore(t)
	m := New(st)
	vms := make([]*state.VMRecord, 24)
	for i := range vms {
		name := fmt.Sprintf("vm-%02d", i)
		vm := &state.VMRecord{
			SchemaVersion: state.SchemaVersion,
			ID:            name, Name: name, Driver: "qemu",
			Network: state.VMNetwork{SSHBind: "127.0.0.1", SSHPort: 2222},
		}
		if err := st.SaveVM(vm); err != nil {
			t.Fatal(err)
		}
		if err := st.RegisterVM(name, name); err != nil {
			t.Fatal(err)
		}
		if err := process.Record(st.Runtime(vm).VMProcessRecord(), os.Getpid()); err != nil {
			t.Fatal(err)
		}
		vms[i] = vm
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ps"), []byte("#!/bin/sh\nprintf 'probe\\n' >> \"$VOOM_PS_PROBES\"\nexec sleep 5\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, budget := range []time.Duration{time.Second, 100 * time.Millisecond} {
		t.Run(budget.String(), func(t *testing.T) {
			probes := filepath.Join(t.TempDir(), "probes")
			t.Setenv("VOOM_PS_PROBES", probes)
			ctx := context.Background()
			if budget < time.Second {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, budget)
				defer cancel()
			}
			start := time.Now()
			results := m.InspectRuntimes(ctx, vms)
			t.Logf("inspected %d VMs in %s with budget %s", len(vms), time.Since(start), budget)
			if elapsed := time.Since(start); elapsed > budget+500*time.Millisecond {
				t.Errorf("process inspection exceeded shared budget %s: %s", budget, elapsed)
			}
			for i, result := range results {
				if result.Status != RuntimeUnknown || !errors.Is(result.RuntimeError, context.DeadlineExceeded) ||
					!strings.Contains(strings.Join(result.ForwardingWarnings, " "), context.DeadlineExceeded.Error()) {
					t.Errorf("VM %d should report unknown status and a deadline error, got %+v", i, result)
				}
			}
			data, err := os.ReadFile(probes)
			if err != nil {
				t.Fatal(err)
			}
			if count := len(strings.Fields(string(data))); count == 0 || count > 8 {
				t.Fatalf("started %d process probes for a blocked batch", count)
			}
		})
	}
}
