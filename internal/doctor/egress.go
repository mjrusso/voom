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
	add := func(kind, severity string, err error) {
		if err != nil {
			checks = append(checks, Check{"egress-" + kind + "-" + record.Name, severity, false, fmt.Sprintf("VM %s (%s): %v", record.Name, record.ID, err)})
		}
	}
	add("declaration", "fatal", egress.Syntax(*d))
	manager := vm.New(st)
	add("assignment", "fatal", manager.CheckEgressAssignment(record, *d))
	var err error
	severity := "warn"
	if d.Enabled {
		severity = "fatal"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	add("backend", severity, egress.ProbeSocket(ctx, d.BackendSocket))
	if !manager.IsRunning(record) || !d.Enabled {
		_, err = egress.ReadCA(d.CACertPath)
		add("ca", severity, err)
	}
	image, err := st.LoadImageByID(record.Image.ID)
	if err == nil && !image.Capabilities.ControlShare {
		err = fmt.Errorf("image lacks controlShare capability")
	}
	add("image", severity, err)
	if manager.IsRunning(record) {
		err = gvproxy.CheckProcess(ctx, st.Runtime(record).NetworkSock(), gvproxy.GuestIsolationCapability, gvproxy.GatewayCapability)
		add("capability", severity, err)
		runtimeSeverity := "fatal"
		if err != nil && !d.Enabled {
			runtimeSeverity = "warn"
		}
		_, err = manager.ObserveEgress(ctx, record)
		add("runtime", runtimeSeverity, err)
		if !d.Enabled {
			for _, path := range []string{st.Runtime(record).EgressManifest(), st.Runtime(record).EgressCA()} {
				if _, err := os.Lstat(path); err == nil {
					add("files", "fatal", fmt.Errorf("disabled attachment has unexpected file %s; run voom config egress disable %s", path, record.Name))
				}
			}
		}
	} else {
		executable, err := host.ExePath("gvproxy")
		if err == nil {
			err = gvproxy.CheckExecutable(ctx, executable, gvproxy.GuestIsolationCapability, gvproxy.GatewayCapability)
		}
		add("capability", severity, err)
		rt := st.Runtime(record)
		if pid, ok := process.ValidRecord(rt.GVProxyProcessRecord(), "gvproxy"); ok {
			add("orphan", "fatal", fmt.Errorf("gvproxy PID %d survives the VM; run voom stop %s and inspect %s", pid, record.Name, st.LogPath(record, "gvproxy")))
		}
		for _, path := range []string{rt.EgressManifest(), rt.EgressCA()} {
			if _, err := os.Lstat(path); err == nil {
				add("files", "warn", fmt.Errorf("stale runtime file %s; run voom config egress disable %s", path, record.Name))
			}
		}
	}
	return checks
}
