package process

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

type recordStatus uint8

const (
	recordGone recordStatus = iota
	recordMatches
	recordWrongKind
)

func inspectRecord(recorded processRecord, kind string) (recordStatus, error) {
	matched, commandErr := matchesKind(recorded.PID, kind)
	current, err := birth(recorded.PID)
	if errors.Is(err, os.ErrNotExist) {
		return recordGone, nil
	}
	if err != nil {
		return recordGone, fmt.Errorf("cannot verify PID %d identity: %w", recorded.PID, err)
	}
	if current != recorded.Birth {
		return recordGone, nil
	}
	if commandErr != nil {
		return recordGone, fmt.Errorf("cannot verify PID %d command: %w", recorded.PID, commandErr)
	}
	if !matched {
		return recordWrongKind, nil
	}
	return recordMatches, nil
}

// ValidRecord confirms that a recorded process is alive, unchanged, and of the expected kind.
func ValidRecord(path, kind string) (int, bool) {
	recorded, err := readRecord(path)
	if err != nil {
		return 0, false
	}
	status, err := inspectRecord(recorded, kind)
	if err != nil || status != recordMatches {
		return 0, false
	}
	return recorded.PID, true
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
	status, err := inspectRecord(recorded, kind)
	if err != nil {
		return err
	}
	if status == recordGone {
		return RemoveRecord(path)
	}
	if status == recordWrongKind {
		return fmt.Errorf("cannot verify %s PID %d executable; retained %s", kind, pid, path)
	}
	sameProcess := func() (bool, error) {
		current, err := birth(pid)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return current == recorded.Birth, nil
	}
	for _, signal := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		same, err := sameProcess()
		if err != nil {
			return fmt.Errorf("cannot verify PID %d identity: %w", pid, err)
		}
		if !same {
			return RemoveRecord(path)
		}
		if err = syscall.Kill(pid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		if WaitFor(func() bool {
			same, err = sameProcess()
			return err == nil && !same
		}, timeout) == nil {
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
