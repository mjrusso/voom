package process

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mjrusso/voom/internal/atomicfile"
)

const recordVersion = 1

type processRecord struct {
	Version int    `json:"version"`
	PID     int    `json:"pid"`
	Birth   string `json:"birth"`
}

func readRecord(path string) (processRecord, error) {
	var recorded processRecord
	data, err := os.ReadFile(path)
	if err != nil {
		return recorded, err
	}
	if err := json.Unmarshal(data, &recorded); err != nil {
		return processRecord{}, err
	}
	if recorded.Version != recordVersion || recorded.PID <= 1 || recorded.Birth == "" {
		return processRecord{}, fmt.Errorf("invalid process record in %s", path)
	}
	return recorded, nil
}

// Record writes the process identity required for status checks and signaling.
func Record(path string, pid int) error {
	value, err := birth(pid)
	if err != nil {
		return err
	}
	data, err := json.Marshal(processRecord{Version: recordVersion, PID: pid, Birth: value})
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(data, '\n'), 0600)
}

// HasRecord reports whether a process record remains.
func HasRecord(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, os.ErrNotExist)
}

// FindRecords returns process records matching pattern.
func FindRecords(pattern string) []string {
	paths, _ := filepath.Glob(pattern)
	return paths
}

// ValidRecord confirms that a recorded process is alive, unchanged, and of the expected kind.
func ValidRecord(path, kind string) (int, bool) {
	recorded, err := readRecord(path)
	if err != nil {
		return 0, false
	}
	current, err := birth(recorded.PID)
	if err != nil || current != recorded.Birth {
		return 0, false
	}
	matched, err := matchesKind(recorded.PID, kind)
	return recorded.PID, err == nil && matched
}

// StopRecorded validates a process record before signaling the process.
func StopRecorded(path, kind string, timeout time.Duration) error {
	recorded, err := readRecord(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	pid := recorded.PID
	if err = syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return RemoveRecord(path)
	} else if err != nil {
		return fmt.Errorf("cannot inspect PID %d: %w", pid, err)
	}
	original, err := birth(pid)
	if errors.Is(err, os.ErrNotExist) {
		return RemoveRecord(path)
	}
	if err != nil {
		return fmt.Errorf("cannot verify PID %d identity: %w", pid, err)
	}
	if original != recorded.Birth {
		return RemoveRecord(path)
	}
	matched, err := matchesKind(pid, kind)
	if err != nil {
		if _, birthErr := birth(pid); errors.Is(birthErr, os.ErrNotExist) {
			return RemoveRecord(path)
		}
		return fmt.Errorf("cannot verify PID %d command: %w", pid, err)
	}
	if !matched {
		return fmt.Errorf("cannot verify %s PID %d executable; retained %s", kind, pid, path)
	}
	exited := func() bool {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return true
		}
		current, err := birth(pid)
		return errors.Is(err, os.ErrNotExist) || (err == nil && current != original)
	}
	for _, signal := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		if exited() {
			return RemoveRecord(path)
		}
		current, err := birth(pid)
		if errors.Is(err, os.ErrNotExist) || (err == nil && current != original) {
			return RemoveRecord(path)
		}
		if err != nil {
			return fmt.Errorf("cannot confirm PID %d identity before signal: %w", pid, err)
		}
		if err = syscall.Kill(pid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		if WaitFor(exited, timeout) == nil {
			return RemoveRecord(path)
		}
	}
	return fmt.Errorf("termination of %s PID %d unconfirmed; retained %s", kind, pid, path)
}

// RemoveRecord removes a process record if it exists.
func RemoveRecord(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
