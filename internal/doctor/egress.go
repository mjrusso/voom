package doctor

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mjrusso/voom/internal/egress"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
	"github.com/mjrusso/voom/internal/vm"
)

func egressDiagnostics(st *state.Store, manager *vm.Manager, record *state.VMRecord) []Check {
	d := record.Network.Egress
	if d == nil {
		return nil
	}
	checks := []Check{}
	add := func(kind string, fatal bool, err error) {
		if err != nil {
			severity := "warn"
			if fatal {
				severity = "fatal"
			}
			checks = append(checks, Check{"egress-" + kind + "-" + record.Name, severity, false, fmt.Sprintf("VM %s (%s): %v", record.Name, record.ID, err)})
		}
	}
	add("declaration", true, egress.Syntax(*d))
	running := manager.IsRunning(record)
	add("assignment", true, manager.CheckEgressAssignment(record, *d))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	add("backend", d.Enabled, egress.ProbeSocket(ctx, d.BackendSocket))
	if !running || !d.Enabled {
		_, caErr := egress.ReadCA(d.CACertPath)
		add("ca", d.Enabled, caErr)
	}
	add("image", d.Enabled, manager.CheckEgressImage(record))
	runtime := st.Runtime(record)
	capabilityErr := manager.CheckEgressCapabilities(ctx, record)
	add("capability", d.Enabled, capabilityErr)
	if running {
		_, runtimeErr := manager.ObserveEgress(ctx, record)
		runtimeFatal := d.Enabled || capabilityErr == nil
		add("runtime", runtimeFatal, runtimeErr)
	} else {
		if pid, ok := process.ValidRecord(runtime.GVProxyProcessRecord(), "gvproxy"); ok {
			add("orphan", true, fmt.Errorf("gvproxy PID %d survives the VM; run voom stop %s and inspect %s", pid, record.Name, st.LogPath(record, "gvproxy")))
		}
	}
	if !running || !d.Enabled {
		problem := "stale runtime file"
		if running {
			problem = "disabled attachment has unexpected file"
		}
		for _, path := range []string{runtime.EgressManifest(), runtime.EgressCA()} {
			if _, err := os.Lstat(path); err == nil {
				add("files", running, fmt.Errorf("%s %s; run voom config egress disable %s", problem, path, record.Name))
			}
		}
	}
	return checks
}
