package vm

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/state"
)

func macosResetFixture(t *testing.T) (*state.Store, *state.VMRecord, *state.ImageRecord) {
	t.Helper()
	st, _ := newTestStore(t)
	bundle := t.TempDir()
	for _, name := range []string{"image.json", "restore.json", "toolchain.json"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "macos-bundle", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bundle, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"disk.raw", "MachineIdentifier", "HardwareModel", "AuxiliaryStorage"} {
		if err := os.WriteFile(filepath.Join(bundle, name), []byte("golden-"+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	im, err := st.ImportImage(context.Background(), state.ImportOptions{Name: "golden", Src: bundle, SSHIdentityPath: "/keys/golden"})
	if err != nil {
		t.Fatal(err)
	}
	oldImage, err := st.ImportImage(context.Background(), state.ImportOptions{Name: "previous", Src: bundle, SSHUser: "old-user", SSHIdentityPath: "/keys/old"})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("old-MachineIdentifier"))
	platform := *im.MacOS
	platform.IdentitySHA256 = hex.EncodeToString(hash[:])
	vm := &state.VMRecord{
		SchemaVersion: 2, ID: "mac", Name: "mac", Arch: im.Arch, Driver: "vfkit",
		MacOS: &platform, Image: state.VMImageRef{ID: oldImage.ID, Name: oldImage.Name},
		Resources: state.VMResources{CPUs: 4, MemoryMiB: 8192},
		Access:    state.VMAccess{SSHUser: "old-user", SSHIdentityPath: "/keys/old"},
		Network:   state.VMNetwork{SSHPort: 2222, SSHBind: "127.0.0.1"},
	}
	vm.StorageGeneration = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := os.MkdirAll(st.VMStorageDir(vm), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVM(vm); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(vm.Name, vm.ID); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"disk.raw", "MachineIdentifier", "HardwareModel", "AuxiliaryStorage"} {
		if err := os.WriteFile(filepath.Join(st.VMStorageDir(vm), name), []byte("old-"+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	helper := filepath.Join(t.TempDir(), "identity-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf 'fresh-%s' \"$1\" > \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VOOM_MACOS_IDENTITY", helper)
	return st, vm, im
}

func TestMacOSResetReplacesCompleteStateAndCloneUsesActiveGeneration(t *testing.T) {
	st, original, im := macosResetFixture(t)
	original.Network.Forwards = []forward.Decl{{Protocol: "tcp", Bind: "127.0.0.1", HostPort: freeLoopbackPort(t), GuestPort: 8000}}
	if err := st.SaveVM(original); err != nil {
		t.Fatal(err)
	}
	manager := New(st)
	for range 2 {
		if err := manager.ResetDisk(context.Background(), "mac", "golden"); err != nil {
			t.Fatal(err)
		}
		vm, err := st.LoadVM("mac")
		if err != nil {
			t.Fatal(err)
		}
		if vm.MacOS.IdentitySHA256 == original.MacOS.IdentitySHA256 || vm.MacOS.IdentitySHA256 == im.MacOS.IdentitySHA256 || vm.StorageGeneration == original.StorageGeneration {
			t.Fatal("reset reused the previous or image identity/storage")
		}
		if expected := vmFromImage(*original, im).Access; vm.Access != expected || vm.Access.NixosTargetUser != "" {
			t.Fatalf("reset access = %+v; want %+v", vm.Access, expected)
		}
		if vm.ID != original.ID || vm.Name != original.Name || !reflect.DeepEqual(vm.Network, original.Network) || vm.Resources != original.Resources {
			t.Fatalf("reset changed host-side identity or configuration: %+v", vm)
		}
		if vm.Access.SSHUser != im.Metadata.SSHUser || vm.Access.SSHIdentityPath != im.Metadata.SSHIdentityPath || vm.Image.ID != im.ID {
			t.Fatalf("reset did not adopt image access metadata: %+v", vm)
		}
		for _, name := range []string{"disk.raw", "HardwareModel", "AuxiliaryStorage"} {
			data, err := os.ReadFile(filepath.Join(st.VMStorageDir(vm), name))
			if err != nil || string(data) != "golden-"+name {
				t.Fatalf("replacement %s: %q %v", name, data, err)
			}
			if _, err := os.Stat(filepath.Join(st.VMStorageDir(original), name)); !os.IsNotExist(err) {
				t.Fatalf("previous %s was retained: %v", name, err)
			}
		}
		if err := vm.MacOS.ValidateStorage(st.VMStorageDir(vm)); err != nil {
			t.Fatal(err)
		}
		vargs, err := manager.vfkitArgs(vm, im, nil)
		if err != nil {
			t.Fatal(err)
		}
		args := strings.Join(vargs, " ")
		if !strings.Contains(args, "auxImagePath="+filepath.Join(st.VMStorageDir(vm), "AuxiliaryStorage")) || !strings.Contains(args, "path="+st.VMDiskPath(vm)) {
			t.Fatalf("boot arguments ignore active generation: %s", args)
		}
		original = vm
	}
	clone, err := manager.Clone(context.Background(), "mac", "clone")
	if err != nil {
		t.Fatal(err)
	}
	if err := clone.MacOS.ValidateStorage(st.VMStorageDir(clone)); err != nil {
		t.Fatal(err)
	}
	if len(clone.Network.Forwards) != 0 {
		t.Fatalf("clone inherited host port reservations: %+v", clone.Network.Forwards)
	}
	if _, err := st.LoadVM("clone"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Remove(context.Background(), "mac"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.VMDir(original.ID)); !os.IsNotExist(err) {
		t.Fatalf("VM storage remains after removal: %v", err)
	}
	if err := clone.MacOS.ValidateStorage(st.VMStorageDir(clone)); err != nil {
		t.Fatalf("removing source damaged clone: %v", err)
	}
	if err := im.MacOS.ValidateStorage(st.ImageDir(im.ID)); err != nil {
		t.Fatalf("reset damaged image: %v", err)
	}
}

func TestMacOSResetFailuresPreserveActiveState(t *testing.T) {
	for _, mode := range []string{"helper", "duplicate identity", "cancelled", "resources", "missing image state", "unknown runtime", "record write"} {
		t.Run(mode, func(t *testing.T) {
			st, vm, im := macosResetFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "helper":
				if err := os.WriteFile(os.Getenv("VOOM_MACOS_IDENTITY"), []byte("#!/bin/sh\nprintf broken > \"$1\"\nexit 1\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "duplicate identity":
				if err := os.WriteFile(os.Getenv("VOOM_MACOS_IDENTITY"), []byte("#!/bin/sh\nprintf old-MachineIdentifier > \"$1\"\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				cancel()
			case "resources":
				im.MacOS.MemoryMiB *= 2
				im.MacOS.MinimumMemoryMiB = im.MacOS.MemoryMiB
				if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(im.ID), "image.json"), im); err != nil {
					t.Fatal(err)
				}
			case "missing image state":
				if err := os.Remove(filepath.Join(st.ImageDir(im.ID), "AuxiliaryStorage")); err != nil {
					t.Fatal(err)
				}
			case "unknown runtime":
				if err := state.WriteJSONAtomic(st.Runtime(vm).VMProcessRecord(), map[string]string{"invalid": "identity"}); err != nil {
					t.Fatal(err)
				}
			case "record write":
				if os.Geteuid() == 0 {
					t.Skip("requires directory permissions to deny writes")
				}
				script := "#!/bin/sh\nprintf fresh > \"$1\"\nchmod 500 \"$(dirname \"$(dirname \"$1\")\")\"\n"
				if err := os.WriteFile(os.Getenv("VOOM_MACOS_IDENTITY"), []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(st.VMDir(vm.ID), 0o700) })
			}
			if err := New(st).ResetDisk(ctx, vm.Name, im.Name); err == nil {
				t.Fatal("reset should fail")
			}
			if err := os.Chmod(st.VMDir(vm.ID), 0o700); err != nil {
				t.Fatal(err)
			}
			saved, err := st.LoadVM(vm.Name)
			if err != nil || !reflect.DeepEqual(saved, vm) {
				t.Fatalf("failed reset changed active record: %+v %v", saved, err)
			}
			if err := saved.MacOS.ValidateStorage(st.VMStorageDir(saved)); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"disk.raw", "MachineIdentifier", "HardwareModel", "AuxiliaryStorage"} {
				data, err := os.ReadFile(filepath.Join(st.VMStorageDir(saved), name))
				if err != nil || string(data) != "old-"+name {
					t.Fatalf("failed reset changed %s: %q %v", name, data, err)
				}
			}
		})
	}
}

func TestMacOSResetRecoveryAtStorageCommitBoundaries(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before commit", true: "after commit"}[committed], func(t *testing.T) {
			st, vm, im := macosResetFixture(t)
			next, err := st.CopyStorageGeneration(context.Background(), vm, im.MacOS, st.ImageDiskPath(im))
			if err != nil {
				t.Fatal(err)
			}
			if committed {
				if err := st.SaveVM(next); err != nil {
					t.Fatal(err)
				}
			}
			interrupted := filepath.Join(st.VMDir(vm.ID), "storage-"+strings.Repeat("e", 32))
			if err := os.Mkdir(interrupted, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(interrupted, "disk.raw.partial"), []byte("partial"), 0o600); err != nil {
				t.Fatal(err)
			}
			reopened, err := state.Open()
			if err != nil {
				t.Fatal(err)
			}
			active, unlock, err := reopened.LockVMRecord(context.Background(), vm.Name)
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			if err := reopened.CleanupStorage(active); err != nil {
				t.Fatal(err)
			}
			if err := active.MacOS.ValidateStorage(reopened.VMStorageDir(active)); err != nil {
				t.Fatal(err)
			}
			expected := vm.MacOS.IdentitySHA256
			if committed {
				expected = next.MacOS.IdentitySHA256
			}
			if active.MacOS.IdentitySHA256 != expected {
				t.Fatal("recovery selected wrong generation")
			}
			if _, err := os.Stat(interrupted); !os.IsNotExist(err) {
				t.Fatalf("partial generation remains: %v", err)
			}
			if !committed {
				if _, err := os.Stat(st.VMStorageDir(next)); !os.IsNotExist(err) {
					t.Fatalf("uncommitted generation remains: %v", err)
				}
			}
		})
	}
}

func holdMacOSStorageLock(t *testing.T, path string) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMacOSStopAndRemoveRetainLockedStorage$")
	cmd.Env = append(os.Environ(), "VOOM_TEST_VM_STORAGE_LOCK="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	release := sync.OnceFunc(func() {
		_ = input.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("lock holder: %v", err)
		}
	})
	t.Cleanup(release)
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || ready != "locked\n" {
		t.Fatalf("storage lock holder: %q %v", ready, err)
	}
	return release
}

func TestMacOSStopAndRemoveRetainLockedStorage(t *testing.T) {
	if path := os.Getenv("VOOM_TEST_VM_STORAGE_LOCK"); path != "" {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = file.Close() }()
		if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintln(os.Stdout, "locked"); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			t.Fatal(err)
		}
		return
	}

	for _, action := range []string{"stop", "remove", "reset"} {
		t.Run(action, func(t *testing.T) {
			st, vm, _ := macosResetFixture(t)
			holdMacOSStorageLock(t, filepath.Join(st.VMStorageDir(vm), "AuxiliaryStorage"))
			var err error
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			manager := New(st)
			switch action {
			case "stop":
				_, err = manager.Stop(ctx, vm.Name)
			case "remove":
				err = manager.Remove(ctx, vm.Name)
			case "reset":
				err = manager.ResetDisk(ctx, vm.Name, "golden")
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("storage lock was ignored: %v", err)
			}
			if _, err := st.LoadVM(vm.Name); err != nil {
				t.Fatalf("locked VM was removed: %v", err)
			}
			if err := vm.MacOS.ValidateStorage(st.VMStorageDir(vm)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResetRejectsGuestOSTransitionsWithoutStoppingVM(t *testing.T) {
	for _, toMac := range []bool{false, true} {
		t.Run(map[bool]string{false: "MacOS to Linux", true: "Linux to MacOS"}[toMac], func(t *testing.T) {
			st, vm, im := macosResetFixture(t)
			if toMac {
				vm.Arch = "aarch64-linux"
				vm.MacOS = nil
				vm.StorageGeneration = ""
				vm.SchemaVersion = 1
				if err := st.SaveVM(vm); err != nil {
					t.Fatal(err)
				}
			} else {
				im.Arch = "aarch64-linux"
				im.MacOS = nil
				im.SchemaVersion = 1
				if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(im.ID), "image.json"), im); err != nil {
					t.Fatal(err)
				}
			}
			rt := st.Runtime(vm)
			if err := state.WriteJSONAtomic(rt.VMProcessRecord(), map[string]string{"unknown": "process"}); err != nil {
				t.Fatal(err)
			}
			err := New(st).ResetDisk(context.Background(), vm.Name, im.Name)
			if err == nil || !strings.Contains(err.Error(), "between Linux and MacOS") {
				t.Fatalf("transition was not rejected before runtime cleanup: %v", err)
			}
			if _, err := os.Stat(rt.VMProcessRecord()); err != nil {
				t.Fatalf("runtime changed: %v", err)
			}
			saved, err := st.LoadVM(vm.Name)
			if err != nil || !reflect.DeepEqual(saved, vm) {
				t.Fatalf("transition changed VM: %+v %v", saved, err)
			}
		})
	}
}

func TestMacOSRemoveIncompleteStorageStillChecksSurvivingLocks(t *testing.T) {
	st, vm, _ := macosResetFixture(t)
	if err := os.Remove(st.VMDiskPath(vm)); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(filepath.Join(st.VMStorageDir(vm), "AuxiliaryStorage"), 0); err != nil {
		t.Fatal(err)
	}
	release := holdMacOSStorageLock(t, filepath.Join(st.VMStorageDir(vm), "AuxiliaryStorage"))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	manager := New(st)
	if err := manager.Remove(ctx, vm.Name); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("incomplete VM's surviving storage lock ignored: %v", err)
	}
	release()
	if err := manager.Remove(context.Background(), vm.Name); err != nil {
		t.Fatalf("unlocked incomplete VM could not be removed: %v", err)
	}
	if _, err := os.Stat(st.VMDir(vm.ID)); !os.IsNotExist(err) {
		t.Fatalf("incomplete storage remains: %v", err)
	}
}

func TestMacOSResetReplacesIncompleteStorage(t *testing.T) {
	for _, missing := range []string{"MachineIdentifier", "HardwareModel", "disk.raw", "AuxiliaryStorage", "generation"} {
		t.Run(missing, func(t *testing.T) {
			st, vm, image := macosResetFixture(t)
			oldDir := st.VMStorageDir(vm)
			path := filepath.Join(oldDir, missing)
			if missing == "generation" {
				path = oldDir
			}
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
			if err := New(st).ResetDisk(context.Background(), vm.Name, image.Name); err != nil {
				t.Fatalf("reset could not recover incomplete storage: %v", err)
			}
			replacement, err := st.LoadVM(vm.Name)
			if err != nil {
				t.Fatal(err)
			}
			if replacement.StorageGeneration == vm.StorageGeneration || replacement.Image.ID != image.ID {
				t.Fatalf("reset did not commit replacement storage: %+v", replacement)
			}
			if err := replacement.MacOS.ValidateStorage(st.VMStorageDir(replacement)); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
				t.Fatalf("incomplete generation survived reset: %v", err)
			}
		})
	}
}
