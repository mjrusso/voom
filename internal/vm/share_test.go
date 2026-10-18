package vm

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mjrusso/voom/internal/state"
)

func TestAddLinuxShareRequiresImageMountCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name         string
		capabilities state.ImageCapabilities
		allowed      bool
	}{
		{"none", state.ImageCapabilities{}, false},
		{"control only", state.ImageCapabilities{ControlShare: true}, false},
		{"mount only", state.ImageCapabilities{GuestShareMount: true}, false},
		{"both", state.ImageCapabilities{ControlShare: true, GuestShareMount: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := newTestStore(t)
			image := &state.ImageRecord{SchemaVersion: 1, ID: "image", Name: "linux", Arch: "x86_64-linux", Format: "qcow2", Disk: "disk.qcow2", Capabilities: tc.capabilities}
			if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(image.ID), "image.json"), image); err != nil {
				t.Fatal(err)
			}
			record := &state.VMRecord{SchemaVersion: 1, ID: "vm", Name: "linux", Arch: image.Arch, Driver: "qemu", Image: state.VMImageRef{ID: image.ID, Name: image.Name}, Resources: state.VMResources{CPUs: 2, MemoryMiB: 2048}}
			if err := st.SaveVM(record); err != nil {
				t.Fatal(err)
			}
			if err := st.RegisterVM(record.Name, record.ID); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(st.VMDir(record.ID), "vm.json"))
			if err != nil {
				t.Fatal(err)
			}
			decl, err := New(st).AddShare(context.Background(), record.Name, "project", t.TempDir(), "/mnt/project", true)
			if tc.allowed {
				if err != nil {
					t.Fatal(err)
				}
				saved, err := st.LoadVM(record.Name)
				if err != nil || len(saved.Shares) != 1 || saved.Shares[0] != decl {
					t.Fatalf("supported share was not saved: %+v %v", saved, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "requires controlShare and guestShareMount") {
				t.Fatalf("unsupported share accepted: %+v %v", decl, err)
			}
			after, err := os.ReadFile(filepath.Join(st.VMDir(record.ID), "vm.json"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("rejected share changed the VM record: %v", err)
			}
		})
	}
}
