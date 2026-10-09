package vm

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/gvproxy"
	"github.com/mjrusso/voom/internal/host"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/state"
)

func (m *Manager) startRecorded(bin string, args []string, logPath, recordPath string) error {
	executable, err := host.ExePath(bin)
	if err != nil {
		return err
	}
	return process.StartRecorded(executable, args, logPath, recordPath)
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

const maxLogLineLen = 300

// driverStartError reports a driver that did not become ready. When the driver
// has exited, the last line of its log usually states why.
func (m *Manager) driverStartError(vm *state.VMRecord, driver string) error {
	logPath := m.store.LogPath(vm, driver)
	if !m.IsRunning(vm) {
		if line := lastLogLine(logPath); line != "" {
			return fmt.Errorf("%s exited during startup: %s; see %s", driver, line, logPath)
		}
	}
	return fmt.Errorf("%s did not start; see %s", driver, logPath)
}

func lastLogLine(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	line := strings.TrimSpace(lines[len(lines)-1])
	if len(line) > maxLogLineLen {
		line = line[:maxLogLineLen] + "..."
	}
	return line
}
