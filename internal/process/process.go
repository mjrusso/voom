package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// StartRecorded launches an executable path and records its identity before releasing it.
func StartRecorded(exe string, args []string, logPath, recordPath string) error {
	return startRecordedExe(exe, args, logPath, recordPath)
}

// StartSelfRecorded launches the current executable and records its identity before releasing it.
func StartSelfRecorded(args []string, logPath, recordPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return startRecordedExe(exe, args, logPath, recordPath)
}

func startRecordedExe(exe string, args []string, logPath, recordPath string) error {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = log, log, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		return err
	}
	if err := Record(recordPath, cmd.Process.Pid); err != nil {
		killErr := cmd.Process.Kill()
		if killErr == nil || errors.Is(killErr, os.ErrProcessDone) {
			_ = cmd.Wait()
		} else {
			_ = cmd.Process.Release()
		}
		_ = log.Close()
		return errors.Join(fmt.Errorf("record process identity in %s: %w", recordPath, err), killErr)
	}
	go func() { _ = cmd.Wait() }()
	_ = log.Close()
	return nil
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

func matchesKind(pid int, kind string) (bool, error) {
	withArgs := kind == "runtime-watch" || kind == "auto-forward"
	parts, err := processInfo(pid, withArgs)
	if err != nil {
		return false, err
	}
	base := filepath.Base(strings.TrimPrefix(parts[0], "."))
	switch kind {
	case "qemu":
		return strings.HasPrefix(base, "qemu-system"), nil
	case "vfkit":
		return strings.Contains(base, "vfkit"), nil
	case "gvproxy":
		return strings.Contains(base, "gvproxy"), nil
	case "virtiofsd":
		return strings.Contains(base, "virtiofsd"), nil
	case "runtime-watch":
		for i := 1; i+1 < len(parts); i++ {
			if parts[i] == "debug" && parts[i+1] == "watch-vm" {
				return true, nil
			}
		}
		return false, nil
	case "auto-forward":
		for i := 1; i+2 < len(parts); i++ {
			if parts[i] == "forward" && parts[i+1] == "auto" && parts[i+2] == "watch" {
				return true, nil
			}
		}
	}
	return false, nil
}

func processInfo(pid int, withArgs bool) ([]string, error) {
	if runtime.GOOS == "linux" {
		cmdline, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
		if err != nil {
			return nil, err
		}
		if len(cmdline) == 0 {
			return nil, fmt.Errorf("empty process command for PID %d", pid)
		}
		parts := strings.Split(string(cmdline), "\x00")
		if !withArgs {
			parts = parts[:1]
		}
		return parts, nil
	}
	field := "comm="
	if withArgs {
		field = "command="
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-ww", "-p", strconv.Itoa(pid), "-o", field).Output()
	line := strings.TrimSpace(string(out))
	if err != nil || line == "" {
		return nil, fmt.Errorf("process command unavailable for PID %d", pid)
	}
	if withArgs {
		return strings.Fields(line), nil
	}
	return []string{line}, nil
}

// Alive reports whether the given PID corresponds to a live process the caller may signal.
func Alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
