package process

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfirmedStopRetainsInvalidProcessRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gvproxy.process.json")
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := StopRecorded(path, "gvproxy", time.Millisecond); err == nil {
		t.Fatal("accepted unknown identity")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("removed recovery record")
	}
}

func TestConfirmedStopUsesProcessRecord(t *testing.T) {
	cmd := fakeNamedProcess(t, "gvproxy")
	path := filepath.Join(t.TempDir(), "gvproxy.process.json")
	if err := Record(path, cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if err := StopRecorded(path, "gvproxy", time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("process record remains after confirmed exit")
	}
}

func TestConfirmedStopDoesNotSignalReusedPID(t *testing.T) {
	cmd := fakeNamedProcess(t, "gvproxy")
	path := filepath.Join(t.TempDir(), "gvproxy.process.json")
	data, _ := json.Marshal(processRecord{Version: recordVersion, PID: cmd.Process.Pid, Birth: "different birth"})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := StopRecorded(path, "gvproxy", time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if !Alive(cmd.Process.Pid) {
		t.Fatal("signaled reused PID")
	}
}

func TestConfirmedStopRetainsUnsupportedRecordVersion(t *testing.T) {
	cmd := fakeNamedProcess(t, "gvproxy")
	path := filepath.Join(t.TempDir(), "gvproxy.process.json")
	data, _ := json.Marshal(processRecord{PID: cmd.Process.Pid, Birth: "legacy"})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := StopRecorded(path, "gvproxy", time.Millisecond); err == nil {
		t.Fatal("accepted unsupported process identity version")
	}
	if !HasRecord(path) {
		t.Fatal("removed unsupported process record")
	}
	if !Alive(cmd.Process.Pid) {
		t.Fatal("signaled process with unsupported identity")
	}
}

func TestConfirmedStopRetainsRecordedWrongKind(t *testing.T) {
	cmd := fakeNamedProcess(t, "unrelated-worker")
	path := filepath.Join(t.TempDir(), "gvproxy.process.json")
	if err := Record(path, cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if err := StopRecorded(path, "gvproxy", time.Millisecond); err == nil {
		t.Fatal("accepted recorded process with wrong executable kind")
	}
	if !HasRecord(path) {
		t.Fatal("removed mismatched process record")
	}
	if !Alive(cmd.Process.Pid) {
		t.Fatal("signaled mismatched process")
	}
}

func TestConfirmedStopRemovesDeadProcessRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gvproxy.process.json")
	data, _ := json.Marshal(processRecord{Version: recordVersion, PID: 2147483647, Birth: "missing"})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := StopRecorded(path, "gvproxy", time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if HasRecord(path) {
		t.Fatal("dead process record remains")
	}
}
