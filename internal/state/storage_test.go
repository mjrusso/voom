package state

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxStorageReplacementRecovery(t *testing.T) {
	for _, driver := range []string{"qemu", "vfkit"} {
		for _, outcome := range []string{"commit", "record failure", "before commit", "after commit"} {
			t.Run(driver+"/"+outcome, func(t *testing.T) {
				st, _ := newTestStore(t)
				vm := &VMRecord{SchemaVersion: 1, ID: "linux", Name: "linux", Driver: driver, Arch: "aarch64-linux", Image: VMImageRef{ID: "old", Name: "old"}, Access: VMAccess{SSHUser: "old-user"}}
				oldDisk := filepath.Join(st.VMDir(vm.ID), "disk."+DiskExtension(driver))
				if driver == "vfkit" {
					oldDisk = filepath.Join(st.VMDir(vm.ID), "disk.img")
				}
				if err := os.MkdirAll(st.VMDir(vm.ID), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(oldDisk, []byte("old-disk"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := st.SaveVM(vm); err != nil {
					t.Fatal(err)
				}
				if err := st.RegisterVM(vm.Name, vm.ID); err != nil {
					t.Fatal(err)
				}
				im := &ImageRecord{SchemaVersion: 1, ID: "new", Name: "new", Arch: vm.Arch, Disk: "disk." + DiskExtension(driver), Metadata: ImageMetadata{SSHUser: "new-user", SSHIdentityPath: "/new/key"}}
				if err := os.MkdirAll(st.ImageDir(im.ID), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(st.ImageDiskPath(im), []byte("new-disk"), 0600); err != nil {
					t.Fatal(err)
				}
				idx := st.IndexSnapshot()
				idx.Images[im.Name] = im.ID
				if err := st.SetIndexSnapshot(idx); err != nil {
					t.Fatal(err)
				}
				candidate := *vm
				candidate.Image = VMImageRef{ID: im.ID, Name: im.Name}
				candidate.Arch = im.Arch
				candidate.Access = VMAccess{SSHUser: "new-user", SSHIdentityPath: "/prepared/key", NixosTargetUser: "switch-user"}
				recordPath := filepath.Join(st.VMDir(vm.ID), "vm.json")
				record, err := os.ReadFile(recordPath)
				if err != nil {
					t.Fatal(err)
				}
				_, unlock, err := st.LockVMRecord(context.Background(), vm.Name)
				if err != nil {
					t.Fatal(err)
				}
				defer unlock()
				switch outcome {
				case "commit":
					if err := st.ReplaceStorage(context.Background(), vm, &candidate, st.ImageDiskPath(im)); err != nil {
						t.Fatal(err)
					}
					if vm.Image != candidate.Image || vm.Access != candidate.Access {
						t.Fatalf("replacement metadata: %#v", vm)
					}
				case "record failure":
					if err := os.Remove(recordPath); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(recordPath, 0700); err != nil {
						t.Fatal(err)
					}
					if err := st.ReplaceStorage(context.Background(), vm, &candidate, st.ImageDiskPath(im)); err == nil {
						t.Fatal("record commit unexpectedly succeeded")
					}
					if data, err := os.ReadFile(oldDisk); err != nil || string(data) != "old-disk" {
						t.Fatalf("failed reset replaced old disk: %q, %v", data, err)
					}
					if vm.Image.ID != "old" || vm.Access.SSHUser != "old-user" {
						t.Fatal("failed reset changed metadata")
					}
					if err := os.Remove(recordPath); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(recordPath, record, 0600); err != nil {
						t.Fatal(err)
					}
				default:
					next, err := st.CopyStorageGeneration(context.Background(), vm, nil, st.ImageDiskPath(im))
					if err != nil {
						t.Fatal(err)
					}
					if outcome == "after commit" {
						if err := st.SaveVM(next); err != nil {
							t.Fatal(err)
						}
					}
				}
				reopened, err := Open()
				if err != nil {
					t.Fatal(err)
				}
				active, err := reopened.LoadVM(vm.Name)
				if err != nil {
					t.Fatal(err)
				}
				if err := reopened.CleanupStorage(active); err != nil {
					t.Fatal(err)
				}
				expected := "old-disk"
				if outcome == "commit" || outcome == "after commit" {
					expected = "new-disk"
					if active.SchemaVersion != 2 || active.StorageGeneration == "" {
						t.Fatal("replacement has no generation")
					}
					if _, err := os.Stat(oldDisk); !os.IsNotExist(err) {
						t.Fatalf("obsolete disk remains: %v", err)
					}
				}
				if data, err := os.ReadFile(reopened.VMDiskPath(active)); err != nil || string(data) != expected {
					t.Fatalf("recovered disk = %q, %v; want %q", data, err, expected)
				}
				entries, err := os.ReadDir(reopened.VMDir(active.ID))
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if entry.IsDir() && entry.Name() != "storage-"+active.StorageGeneration {
						t.Fatalf("orphan storage remains: %s", entry.Name())
					}
				}
			})
		}
	}
}
