package process

import (
	"bytes"
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

// ExeResolver resolves a logical binary name to an absolute path on disk.
type ExeResolver func(string) (string, error)

// StartRecorded launches a detached process and records its identity before releasing it.
func StartRecorded(bin string, args []string, logPath, recordPath string, resolve ExeResolver) error {
	exe, err := resolve(bin)
	if err != nil {
		return err
	}
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
	_ = cmd.Process.Release()
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
	cmdline, base, err := processInfo(pid)
	if err != nil {
		return false, err
	}
	switch kind {
	case "qemu":
		return strings.HasPrefix(base, "qemu-system"), nil
	case "vfkit":
		return strings.Contains(base, "vfkit"), nil
	case "gvproxy":
		return strings.Contains(base, "gvproxy"), nil
	case "virtiofsd":
		return strings.Contains(base, "virtiofsd"), nil
	case "auto-forward":
		parts := bytes.Split(cmdline, []byte{0})
		for i := 1; i+2 < len(parts); i++ {
			if string(parts[i]) == "forward" && string(parts[i+1]) == "auto" && string(parts[i+2]) == "watch" {
				return true, nil
			}
		}
	}
	return false, nil
}

func processInfo(pid int) ([]byte, string, error) {
	if runtime.GOOS == "linux" {
		cmdline, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
		if err != nil {
			return nil, "", err
		}
		if len(cmdline) == 0 {
			return nil, "", fmt.Errorf("empty process command for PID %d", pid)
		}
		first := strings.TrimPrefix(string(bytes.Split(cmdline, []byte{0})[0]), ".")
		return cmdline, filepath.Base(first), nil
	}
	// Defensive timeout: ps should be near-instant; if it hangs, don't block forever.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return nil, "", fmt.Errorf("process command unavailable for PID %d", pid)
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return nil, "", fmt.Errorf("process command unavailable for PID %d", pid)
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil, "", fmt.Errorf("process command unavailable for PID %d", pid)
	}
	return []byte(strings.Join(fields, "\x00")), filepath.Base(strings.TrimPrefix(fields[0], ".")), nil
}

// Alive reports whether the given PID corresponds to a live process the caller may signal.
func Alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
