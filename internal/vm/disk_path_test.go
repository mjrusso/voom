package vm

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mjrusso/voom/internal/state"
)

func TestLegacyVFKitDiskCanGrowAndCloneToRaw(t *testing.T) {
	st, _ := newTestStore(t)
	record := &state.VMRecord{SchemaVersion: state.SchemaVersion, ID: "legacy", Name: "legacy", Driver: "vfkit", Arch: "aarch64-linux", Resources: state.VMResources{CPUs: 2, MemoryMiB: 2048}}
	if err := st.SaveVM(record); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(record.Name, record.ID); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(st.VMDir(record.ID), "disk.img")
	if err := os.WriteFile(legacy, []byte("guest data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := st.VMDiskPath(record); got != legacy {
		t.Fatalf("legacy disk resolved to %s", got)
	}
	manager := New(st)
	if err := manager.GrowDisk(context.Background(), record.Name, "1MiB"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(len("guest data"))+(1<<20) {
		t.Fatalf("legacy disk size: %d", info.Size())
	}
	clone, err := manager.Clone(context.Background(), record.Name, "new")
	if err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(st.VMStorageDir(clone), "disk.raw")
	if got := st.VMDiskPath(clone); got != canonical {
		t.Fatalf("new clone disk resolved to %s", got)
	}
	data, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != int(info.Size()) || string(data[:len("guest data")]) != "guest data" {
		t.Fatal("clone did not preserve legacy disk contents")
	}
	if _, err := os.Stat(filepath.Join(st.VMDir(record.ID), "disk.raw")); !os.IsNotExist(err) {
		t.Fatalf("legacy source was renamed: %v", err)
	}
}
