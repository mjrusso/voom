package process

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ExeResolver resolves a logical binary name to an absolute path on disk.
type ExeResolver func(string) (string, error)

// StartDetached resolves bin via resolve and launches it as a detached background process logging to logPath.
func StartDetached(bin string, args []string, logPath string, resolve ExeResolver) (*exec.Cmd, error) {
	exe, err := resolve(bin)
	if err != nil {
		return nil, err
	}
	return StartDetachedExe(exe, args, logPath)
}

// StartSelfDetached re-executes the current binary in a detached child process logging to logPath.
func StartSelfDetached(args []string, logPath string) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return StartDetachedExe(exe, args, logPath)
}

// StartDetachedExe spawns exe as a new session leader (detached from the parent), redirecting stdout/stderr to logPath.
func StartDetachedExe(exe string, args []string, logPath string) (*exec.Cmd, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, err
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = log, log, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		return nil, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	if proc, err := os.FindProcess(pid); err == nil {
		cmd.Process = proc
	}
	_ = log.Close()
	return cmd, nil
}

// WaitFor polls ok every 100ms until it returns true or d elapses, returning context.DeadlineExceeded on timeout.
func WaitFor(ok func() bool, d time.Duration) error {
	return WaitForContext(context.Background(), ok, d)
}

// WaitForContext polls ok every 100ms until it returns true, ctx is cancelled, or d elapses.
// Returns ctx.Err() on cancellation and context.DeadlineExceeded on timeout.
func WaitForContext(ctx context.Context, ok func() bool, d time.Duration) error {
	deadline := time.Now().Add(d)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for time.Now().Before(deadline) {
		if ok() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
	if ok() {
		return nil
	}
	return context.DeadlineExceeded
}

// ValidPID reads path, returning the recorded PID and whether the process is alive AND its executable matches kind ("qemu", "vfkit", "gvproxy", "virtiofsd", "auto-forward").
func ValidPID(path, kind string) (int, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || !Alive(pid) {
		return 0, false
	}
	cmdline, base := processInfo(pid)
	if base == "" {
		return pid, false
	}
	switch kind {
	case "qemu":
		return pid, strings.HasPrefix(base, "qemu-system")
	case "vfkit":
		return pid, strings.Contains(base, "vfkit")
	case "gvproxy":
		return pid, strings.Contains(base, "gvproxy")
	case "virtiofsd":
		return pid, strings.Contains(base, "virtiofsd")
	case "auto-forward":
		parts := bytes.Split(cmdline, []byte{0})
		for i := 1; i+2 < len(parts); i++ {
			if string(parts[i]) == "forward" && string(parts[i+1]) == "auto" && string(parts[i+2]) == "watch" {
				return pid, true
			}
		}
	}
	return pid, false
}

func processInfo(pid int) ([]byte, string) {
	if runtime.GOOS == "linux" {
		cmdline, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
		first := strings.TrimPrefix(string(bytes.Split(cmdline, []byte{0})[0]), ".")
		return cmdline, filepath.Base(first)
	}
	// Defensive timeout: ps should be near-instant; if it hangs, don't block forever.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return nil, ""
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return nil, ""
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil, ""
	}
	return []byte(strings.Join(fields, "\x00")), filepath.Base(strings.TrimPrefix(fields[0], "."))
}

// Alive reports whether the given PID corresponds to a live process the caller may signal.
func Alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// StopPidfile reads the pidfile, sends SIGTERM (escalating to SIGKILL after graceful) if the process matches kind, then removes the pidfile.
func StopPidfile(path, kind string, graceful time.Duration) {
	if pid, ok := ValidPID(path, kind); ok {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		_ = WaitFor(func() bool { return !Alive(pid) }, graceful)
		if Alive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	_ = os.Remove(path)
}
