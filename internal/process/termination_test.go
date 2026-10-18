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

func TestObserveRecordDistinguishesExitFromUncertainty(t *testing.T) {
	for _, mode := range []string{"running", "exited", "wrong-kind", "invalid-record", "reused-pid", "replaced", "removed", "invalid-fingerprint"} {
		t.Run(mode, func(t *testing.T) {
			cmd := fakeNamedProcess(t, "gvproxy")
			path := filepath.Join(t.TempDir(), "process.json")
			if err := Record(path, cmd.Process.Pid); err != nil {
				t.Fatal(err)
			}
			fingerprint, err := RecordFingerprint(path)
			if err != nil {
				t.Fatal(err)
			}
			kind := "gvproxy"
			want := RecordRunning
			wantError := false
			switch mode {
			case "exited":
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				_, _ = cmd.Process.Wait()
				want = RecordExited
			case "wrong-kind":
				kind, want, wantError = "vfkit", RecordUnknown, true
			case "invalid-record":
				if err := os.WriteFile(path, []byte("unknown identity"), 0o600); err != nil {
					t.Fatal(err)
				}
				fingerprint, err = RecordFingerprint(path)
				if err != nil {
					t.Fatal(err)
				}
				want, wantError = RecordUnknown, true
			case "reused-pid":
				data, err := json.Marshal(processRecord{Version: recordVersion, PID: cmd.Process.Pid, Birth: "different birth"})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				fingerprint, err = RecordFingerprint(path)
				if err != nil {
					t.Fatal(err)
				}
				want = RecordExited
			case "replaced":
				other := fakeNamedProcess(t, "gvproxy")
				if err := Record(path, other.Process.Pid); err != nil {
					t.Fatal(err)
				}
				want = RecordChanged
			case "removed":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				want = RecordChanged
			case "invalid-fingerprint":
				fingerprint, want, wantError = "not-an-identity", RecordUnknown, true
			}
			observed, err := ObserveRecord(path, kind, fingerprint)
			if observed.State != want || (err != nil) != wantError {
				t.Fatalf("observation = %+v %v; want state %d, error %v", observed, err, want, wantError)
			}
			if mode == "running" && (observed.PID != cmd.Process.Pid || observed.Fingerprint != fingerprint) {
				t.Fatalf("observation lost running identity: %+v", observed)
			}
			if mode != "exited" && !Alive(cmd.Process.Pid) {
				t.Fatal("observation signaled the live process")
			}
		})
	}
}
