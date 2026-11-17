package state

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestVMNamesComeFromIndex(t *testing.T) {
	st, _ := newTestStore(t)
	vm := &VMRecord{SchemaVersion: SchemaVersion, ID: "vm", Name: "legacy-name", Driver: "qemu"}
	if err := st.SaveVM(vm); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM("indexed-name", vm.ID); err != nil {
		t.Fatal(err)
	}
	check := func(name string) {
		t.Helper()
		loaded, err := st.LoadVM(name)
		if err != nil || loaded.Name != name {
			t.Fatalf("LoadVM(%q) = %+v, %v", name, loaded, err)
		}
		loaded, err = st.LoadVMByID(vm.ID)
		if err != nil || loaded.Name != name {
			t.Fatalf("LoadVMByID = %+v, %v", loaded, err)
		}
		locked, unlock, err := st.LockVMRecord(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		unlock()
		if locked.Name != name {
			t.Fatalf("locked name = %q", locked.Name)
		}
	}
	check("indexed-name")
	path := filepath.Join(st.VMDir(vm.ID), "vm.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RenameVM(context.Background(), "indexed-name", "renamed"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rename rewrote vm.json: %v", err)
	}
	st, err = Open()
	if err != nil {
		t.Fatal(err)
	}
	check("renamed")
	if _, err := st.LoadVM("indexed-name"); err == nil {
		t.Fatal("old name remains registered")
	}
}
