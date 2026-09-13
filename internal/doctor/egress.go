package doctor

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mjrusso/voom/internal/egress"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/host"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
	"github.com/mjrusso/voom/internal/vm"
)

func egressDiagnostics(st *state.Store, record *state.VMRecord) []Check {
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
	manager := vm.New(st)
	running := manager.IsRunning(record)
	add("assignment", true, manager.CheckEgressAssignment(record, *d))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	add("backend", d.Enabled, egress.ProbeSocket(ctx, d.BackendSocket))
	if !running || !d.Enabled {
		_, caErr := egress.ReadCA(d.CACertPath)
		add("ca", d.Enabled, caErr)
	}
	image, imageErr := st.LoadImageByID(record.Image.ID)
	if imageErr == nil && !image.Capabilities.ControlShare {
		imageErr = fmt.Errorf("image lacks controlShare capability")
	}
	add("image", d.Enabled, imageErr)
	runtime := st.Runtime(record)
	if running {
		capabilityErr := gvproxy.CheckProcess(ctx, runtime.NetworkSock(), gvproxy.GuestIsolationCapability, gvproxy.GatewayCapability)
		add("capability", d.Enabled, capabilityErr)
		_, runtimeErr := manager.ObserveEgress(ctx, record)
		runtimeFatal := d.Enabled || capabilityErr == nil
		add("runtime", runtimeFatal, runtimeErr)
	} else {
		executable, capabilityErr := host.ExePath("gvproxy")
		if capabilityErr == nil {
			capabilityErr = gvproxy.CheckExecutable(ctx, executable, gvproxy.GuestIsolationCapability, gvproxy.GatewayCapability)
		}
		add("capability", d.Enabled, capabilityErr)
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
