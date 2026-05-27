package vm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/host"
	"github.com/mjrusso/voom/internal/process"
)

func startDetached(bin string, args []string, logPath string) (*exec.Cmd, error) {
	return process.StartDetached(bin, args, logPath, host.ExePath)
}

func waitFor(ctx context.Context, ok func() bool, d time.Duration) error {
	return process.WaitForContext(ctx, ok, d)
}

func validPid(path, kind string) (int, bool) {
	return process.ValidPID(path, kind)
}

func processAlive(pid int) bool {
	return process.Alive(pid)
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
