//go:build darwin

package process

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Darwin's sys/proc.h defines SZOMB as 5.
const darwinZombieState = 5

func birth(pid int) (string, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	missing := errors.Is(err, unix.ESRCH)
	if errors.Is(err, unix.EIO) {
		missing = errors.Is(unix.Kill(pid, 0), unix.ESRCH)
	}
	if missing {
		return "", os.ErrNotExist
	}
	if err != nil {
		return "", err
	}
	if int(info.Proc.P_pid) != pid || info.Proc.P_stat == darwinZombieState {
		return "", os.ErrNotExist
	}
	started := info.Proc.P_starttime
	if started.Sec <= 0 || started.Usec < 0 {
		return "", errors.New("process start identity unavailable")
	}
	return fmt.Sprintf("%d:%06d", started.Sec, started.Usec), nil
}
