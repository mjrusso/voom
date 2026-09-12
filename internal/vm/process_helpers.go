package vm

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/host"
	"github.com/mjrusso/voom/internal/process"
)

func (m *Manager) startRecorded(bin string, args []string, logPath, recordPath string) error {
	return process.StartRecorded(bin, args, logPath, recordPath, host.ExePath)
}

func waitFor(ctx context.Context, ok func() bool, d time.Duration) error {
	return process.WaitForContext(ctx, ok, d)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func socketExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}

func globFiles(pattern string) []string {
	out, _ := filepath.Glob(pattern)
	return out
}

func portBusy(bind string, port int) bool {
	ok, _ := forward.HostPortAvailable(bind, port)
	return !ok
}

func gvproxyExpose(sock, local, remote string) error {
	return gvproxy.Expose(sock, local, remote)
}

func gvproxyUnexpose(sock, local string) error {
	return gvproxy.Unexpose(sock, local)
}
