package vm

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mjrusso/voom/internal/state"
)

func TestMacOSOperationsRejectBeforeChangingStorage(t *testing.T) {
	st, _ := newTestStore(t)
	record := &state.VMRecord{SchemaVersion: 2, ID: "mac", Name: "mac", Arch: "aarch64-darwin", Driver: "vfkit", MacOS: &state.MacOSPlatform{IdentitySHA256: strings.Repeat("a", 64), CPUs: 4, MemoryMiB: 8192, Version: "27.0.1", Build: "26A434"}, Resources: state.VMResources{CPUs: 4, MemoryMiB: 8192}}
	record.StorageGeneration = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := os.MkdirAll(st.VMStorageDir(record), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVM(record); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(record.Name, record.ID); err != nil {
		t.Fatal(err)
	}
	disk := st.VMDiskPath(record)
	if disk != filepath.Join(st.VMStorageDir(record), "disk.raw") {
		t.Fatalf("macOS disk path: %s", disk)
	}
	if err := os.WriteFile(disk, []byte("golden"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := New(st)
	ctx := context.Background()
	tests := []struct {
		name string
		run  func() error
	}{
		{"missing reset image", func() error { return manager.ResetDisk(ctx, "mac", "missing") }},
		{"grow", func() error { return manager.GrowDisk(ctx, "mac", "1GiB") }},
		{"CPU floor", func() error { _, _, err := manager.SetCPUs(ctx, "mac", 2); return err }},
		{"memory floor", func() error { _, _, err := manager.SetMemory(ctx, "mac", 4096); return err }},
		{"share", func() error { _, err := manager.AddShare(ctx, "mac", "src", t.TempDir(), "/src", false); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); err == nil {
				t.Fatal("unsupported operation succeeded")
			}
			contents, err := os.ReadFile(disk)
			if err != nil || string(contents) != "golden" {
				t.Fatalf("disk changed: %q %v", contents, err)
			}
			saved, err := st.LoadVM("mac")
			if err != nil || saved.Resources.CPUs != 4 || saved.Resources.MemoryMiB != 8192 || len(saved.Shares) != 0 {
				t.Fatalf("record changed: %+v %v", saved, err)
			}
		})
	}
	if _, err := manager.Start(ctx, io.Discard, "mac"); err == nil || !strings.Contains(err.Error(), "MachineIdentifier") {
		t.Fatalf("incomplete platform was not rejected before host/runtime setup: %v", err)
	}
	if _, err := os.Stat(st.RuntimeVMDir(record)); !os.IsNotExist(err) {
		t.Fatalf("failed start created runtime state: %v", err)
	}
	vargs, err := manager.vfkitArgs(record, &state.ImageRecord{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(vargs, " ")
	if !strings.Contains(args, "auxImagePath="+filepath.Join(st.VMStorageDir(record), "AuxiliaryStorage")) || strings.Contains(args, "seed.img") || strings.Contains(args, "variable-store") {
		t.Fatalf("vfkit platform arguments: %s", args)
	}
}

func TestMacOSResourceChangesUseAppleMinimumsAndResetRevalidates(t *testing.T) {
	st, record, legacyImage := macOSResetFixture(t)
	record.MacOS.MinimumCPUs = 2
	record.MacOS.MinimumMemoryMiB = 4096
	if err := st.SaveVM(record); err != nil {
		t.Fatal(err)
	}
	manager := New(st)
	if _, _, err := manager.SetCPUs(context.Background(), record.Name, 2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.SetMemory(context.Background(), record.Name, 4096); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.SetMemory(context.Background(), record.Name, 4095); err == nil {
		t.Fatal("invalid allocation accepted")
	}
	if err := manager.ResetDisk(context.Background(), record.Name, legacyImage.Name); err == nil {
		t.Fatal("reset accepted insufficient resources for legacy image")
	}
	saved, err := st.LoadVM(record.Name)
	if err != nil || saved.Resources.CPUs != 2 || saved.Resources.MemoryMiB != 4096 || saved.Image.ID != record.Image.ID {
		t.Fatalf("resource/reset state changed: %+v %v", saved, err)
	}
}
