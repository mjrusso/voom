package doctor

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mjrusso/voom/internal/state"
)

func TestStateDiagnosticsUsesIndexNamesAfterRename(t *testing.T) {
	for _, scenario := range []string{"renamed", "reused name", "orphan with reused name hint"} {
		t.Run(scenario, func(t *testing.T) {
			st := vmNamesTestStore(t)
			original := &state.VMRecord{SchemaVersion: state.SchemaVersion, ID: "original", Name: "old", Driver: "qemu"}
			if err := st.SaveVM(original); err != nil {
				t.Fatal(err)
			}
			if err := st.RegisterVM(original.Name, original.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := st.RenameVM(context.Background(), "old", "new"); err != nil {
				t.Fatal(err)
			}
			if scenario != "renamed" {
				replacement := *original
				replacement.ID = "replacement"
				if err := st.SaveVM(&replacement); err != nil {
					t.Fatal(err)
				}
				if err := st.RegisterVM(replacement.Name, replacement.ID); err != nil {
					t.Fatal(err)
				}
			}
			wantOrphan := scenario == "orphan with reused name hint"
			if wantOrphan {
				orphan := *original
				orphan.ID = "unregistered"
				if err := st.SaveVM(&orphan); err != nil {
					t.Fatal(err)
				}
			}
			foundOrphan := false
			for _, check := range StateDiagnostics(st) {
				if !strings.HasPrefix(check.Name, "state-vm-") {
					continue
				}
				if wantOrphan && check.Name == "state-vm-orphan-old" {
					foundOrphan = true
					continue
				}
				t.Errorf("false VM corruption diagnostic: %+v", check)
			}
			if foundOrphan != wantOrphan {
				t.Fatalf("orphan hint lost: found=%t, want=%t", foundOrphan, wantOrphan)
			}
		})
	}
}

func TestStateDiagnosticsReportsDuplicateVMIDs(t *testing.T) {
	for _, duplicate := range []string{"index alias", "object copy"} {
		t.Run(duplicate, func(t *testing.T) {
			st := vmNamesTestStore(t)
			vm := &state.VMRecord{SchemaVersion: state.SchemaVersion, ID: "original", Name: "legacy", Driver: "qemu"}
			if err := st.SaveVM(vm); err != nil {
				t.Fatal(err)
			}
			if err := st.RegisterVM("current", vm.ID); err != nil {
				t.Fatal(err)
			}
			if duplicate == "index alias" {
				if err := st.RegisterVM("alias", vm.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := state.WriteJSONAtomic(filepath.Join(st.VMDir("copied-directory"), "vm.json"), vm); err != nil {
					t.Fatal(err)
				}
			}
			for _, check := range StateDiagnostics(st) {
				if check.Name == "state-vm-duplicate-id" {
					return
				}
			}
			t.Fatal("duplicate VM ID was not reported")
		})
	}
}

func vmNamesTestStore(t *testing.T) *state.Store {
	t.Helper()
	dir := t.TempDir()
	for _, kind := range []string{"STATE", "CONFIG", "CACHE", "RUNTIME"} {
		t.Setenv("VOOM_"+kind+"_DIR", filepath.Join(dir, kind))
	}
	st, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	return st
}
