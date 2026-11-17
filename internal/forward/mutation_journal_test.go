package forward

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadMutationJournalPreservesAbsence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		data    string
		present bool
		invalid bool
	}{
		{name: "missing"},
		{name: "empty legacy journal", data: "[]", present: true},
		{name: "malformed journal", data: "{}", present: true, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "journal")
			if tc.present {
				if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			journal, err := ReadMutationJournal(path)
			if tc.invalid {
				if err == nil || journal != nil {
					t.Fatalf("malformed journal returned a snapshot: %+v, %v", journal, err)
				}
				return
			}
			if err != nil || (journal != nil) != tc.present {
				t.Fatalf("journal presence = %+v, %v; want %t", journal, err, tc.present)
			}
			if journal != nil && journal.Version != 0 {
				t.Fatalf("legacy journal version = %d", journal.Version)
			}
		})
	}
}

func TestMutationJournalUsesNormalizedIntentAndPhase(t *testing.T) {
	const data = `{"version":1,"process":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","manual":{"kind":"manual-expose","endpoint":{"protocol":"tcp","bind":"127.0.0.1","hostPort":18080,"guestPort":8080}},"requests":[{"id":"request-1","kind":"manual-expose","endpoint":{"protocol":"tcp","bind":"127.0.0.1","hostPort":18080,"guestPort":8080},"guestTargetIP":"192.168.127.2","result":{"phase":"acknowledged-succeeded","status":200}}]}`
	path := filepath.Join(t.TempDir(), "journal")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	journal, err := ReadMutationJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	request := journal.Requests[0]
	if request.Kind != ManualExpose || request.Result.Phase != MutationAcknowledgedSucceeded || request.Result.Status != 200 || len(journal.Reservations) != 1 || journal.Reservations[0] != request.Endpoint {
		t.Fatalf("normalized intent lost: %+v", journal)
	}
	encoded, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal([]byte(data), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("journal changed representation: %s", encoded)
	}
	for _, tc := range []struct{ name, old, replacement string }{
		{"invalid fingerprint", `"process":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, `"process":"not-an-identity"`},
		{"failure with successful status", `"acknowledged-succeeded"`, `"completed-failed"`},
		{"rejection with successful status", `"acknowledged-succeeded"`, `"acknowledged-rejected"`},
		{"contradictory completion", `"status":200`, `"status":500`},
		{"invalid kind", `"kind":"manual-expose"`, `"kind":"invalid"`},
		{"invalid phase", `"phase":"acknowledged-succeeded"`, `"phase":"invalid"`},
		{"missing phase", `"phase":"acknowledged-succeeded",`, ""},
		{"missing completed status", `,"status":200`, ""},
		{"invalid status", `"status":200`, `"status":600`},
		{"unsubmitted status", `"phase":"acknowledged-succeeded"`, `"phase":"not-submitted"`},
		{"parallel result model", `"phase":"acknowledged-succeeded"`, `"state":"acknowledged","outcome":"succeeded"`},
		{"parallel intent model", `"kind":"manual-expose"`, `"action":"expose","auto":false`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(strings.ReplaceAll(data, tc.old, tc.replacement)), 0600); err != nil {
				t.Fatal(err)
			}
			if journal, err := ReadMutationJournal(path); err == nil || journal != nil {
				t.Fatalf("invalid journal accepted: %+v, %v", journal, err)
			}
		})
	}
}

func TestMutationJournalRetainsLegacyReservations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	if err := os.WriteFile(path, []byte(`[{"protocol":"tcp","bind":"127.0.0.1","hostPort":18080,"guestPort":8080}]`), 0600); err != nil {
		t.Fatal(err)
	}
	journal, err := ReadMutationJournal(path)
	want := []Decl{{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 18080, GuestPort: 8080}}
	if err != nil || journal == nil || journal.Version != 0 || !reflect.DeepEqual(journal.Reservations, want) {
		t.Fatalf("legacy reservations lost: %+v, %v", journal, err)
	}
}
