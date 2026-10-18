package process

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/mjrusso/voom/internal/atomicfile"
)

const recordVersion = 1

var errProcessExited = errors.New("process exited")

type processRecord struct {
	Version int    `json:"version"`
	PID     int    `json:"pid"`
	Birth   string `json:"birth"`
}

func readRecord(path string) (processRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return processRecord{}, err
	}
	return decodeRecord(path, data)
}

func decodeRecord(path string, data []byte) (processRecord, error) {
	var recorded processRecord
	if err := json.Unmarshal(data, &recorded); err != nil {
		return processRecord{}, err
	}
	if recorded.Version != recordVersion || recorded.PID <= 1 || recorded.Birth == "" {
		return processRecord{}, fmt.Errorf("invalid process record in %s", path)
	}
	return recorded, nil
}

// RecordState distinguishes confirmed process observations from uncertainty.
type RecordState uint8

// Record states returned by ObserveRecord.
const (
	RecordUnknown RecordState = iota
	RecordRunning
	RecordExited
	// RecordChanged means the expected record was replaced or removed; it does not confirm exit.
	RecordChanged
)

// Observation binds process state to the record inspected. Only RecordRunning permits use of PID.
type Observation struct {
	State       RecordState
	PID         int
	Fingerprint string
}

func fingerprint(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// RecordFingerprint identifies a process record's contents without inspecting its process.
func RecordFingerprint(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return fingerprint(data), nil
}

// ObserveRecord compares an optional expected fingerprint before inspecting the process.
// Inspection errors return RecordUnknown; only RecordExited confirms the recorded process is gone.
func ObserveRecord(path, kind, expectedFingerprint string) (Observation, error) {
	if expectedFingerprint != "" {
		decoded, err := hex.DecodeString(expectedFingerprint)
		if err != nil || len(decoded) != sha256.Size {
			return Observation{}, errors.New("invalid process record fingerprint")
		}
	}
	data, err := os.ReadFile(path)
	if expectedFingerprint != "" && errors.Is(err, os.ErrNotExist) {
		return Observation{State: RecordChanged}, nil
	}
	if err != nil {
		return Observation{}, err
	}
	observed := Observation{Fingerprint: fingerprint(data)}
	if expectedFingerprint != "" && observed.Fingerprint != expectedFingerprint {
		observed.State = RecordChanged
		return observed, nil
	}
	recorded, err := decodeRecord(path, data)
	if err != nil {
		return Observation{}, err
	}
	status, err := inspectRecord(recorded, kind)
	if err != nil {
		return Observation{}, err
	}
	observed.State = status
	if status == RecordRunning {
		observed.PID = recorded.PID
	}
	return observed, nil
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

func inspectRecord(recorded processRecord, kind string) (RecordState, error) {
	matched, commandErr := matchesKind(recorded.PID, kind)
	current, err := birth(recorded.PID)
	if errors.Is(err, errProcessExited) {
		return RecordExited, nil
	}
	if err != nil {
		return RecordUnknown, fmt.Errorf("cannot verify PID %d identity: %w", recorded.PID, err)
	}
	if current != recorded.Birth {
		return RecordExited, nil
	}
	if commandErr != nil {
		return RecordUnknown, fmt.Errorf("cannot verify PID %d command: %w", recorded.PID, commandErr)
	}
	if !matched {
		return RecordUnknown, fmt.Errorf("cannot verify %s PID %d executable", kind, recorded.PID)
	}
	return RecordRunning, nil
}

// ValidRecord confirms that a recorded process is alive, unchanged, and of the expected kind.
func ValidRecord(path, kind string) (int, bool) {
	observed, err := ObserveRecord(path, kind, "")
	if err != nil || observed.State != RecordRunning {
		return 0, false
	}
	return observed.PID, true
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
	if status == RecordExited {
		return RemoveRecord(path)
	}
	sameProcess := func() (bool, error) {
		current, err := birth(pid)
		if errors.Is(err, errProcessExited) {
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
