package state

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func macOSBundle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"image.json", "restore.json", "toolchain.json"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "macos-bundle", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"disk.raw", "MachineIdentifier", "HardwareModel", "AuxiliaryStorage"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestImportMacOSBundleAndIndependentPlatformCopy(t *testing.T) {
	st, _ := newTestStore(t)
	src := macOSBundle(t)
	im, err := st.ImportImage(context.Background(), ImportOptions{Name: "macos", Src: src, ExperimentalMacOS: true, SSHIdentityPath: "/keys/guest"})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := st.LoadImage("macos")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.MacOS == nil || loaded.MacOS.Version != "27.0.1" || loaded.MacOS.Build != "26A434" || loaded.MacOS.CPUs != 4 || loaded.MacOS.MemoryMiB != 8192 || loaded.Metadata.SSHUser != "guest" || loaded.Metadata.SSHIdentityPath != "/keys/guest" || loaded.Format != "raw" || loaded.SchemaVersion != 2 {
		t.Fatalf("imported image: %+v", loaded)
	}
	for _, name := range []string{"disk.raw", "MachineIdentifier", "HardwareModel", "AuxiliaryStorage", "restore.json", "toolchain.json", "bundle.json"} {
		original := name
		if name == "bundle.json" {
			original = "image.json"
		}
		before, err := os.ReadFile(filepath.Join(src, original))
		if err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(filepath.Join(st.ImageDir(im.ID), name))
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatalf("%s changed during import", name)
		}
	}
	dst := t.TempDir()
	if err := CopyMacOSPlatform(context.Background(), st.ImageDir(im.ID), dst); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "AuxiliaryStorage"), []byte("guest write"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(st.ImageDir(im.ID), "AuxiliaryStorage"))
	if err != nil || string(data) != "AuxiliaryStorage" {
		t.Fatalf("guest changed image storage: %q %v", data, err)
	}
	vm := &VMRecord{SchemaVersion: 2, ID: "mac-vm", Name: "mac", Arch: im.Arch, Driver: "vfkit", Image: VMImageRef{ID: im.ID}, MacOS: im.MacOS, Resources: VMResources{CPUs: 4, MemoryMiB: 8192}}
	if err := st.SaveVM(vm); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(vm.Name, vm.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.WithGlobal(func() error { return st.CheckMacOSIdentityAvailable(im.MacOS) }); err == nil || !strings.Contains(err.Error(), "already assigned") {
		t.Fatalf("duplicate identity allowed: %v", err)
	}
}

func TestMacOSBundleRejectsInvalidInputsWithoutRegistration(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, string)
		opts   ImportOptions
	}{
		{name: "opt-in required"},
		{name: "missing NVRAM", change: func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "AuxiliaryStorage")); err != nil {
				t.Fatal(err)
			}
		}, opts: ImportOptions{ExperimentalMacOS: true}},
		{name: "symlink disk", change: func(t *testing.T, dir string) {
			if err := os.Rename(filepath.Join(dir, "disk.raw"), filepath.Join(dir, "other")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("other", filepath.Join(dir, "disk.raw")); err != nil {
				t.Fatal(err)
			}
		}, opts: ImportOptions{ExperimentalMacOS: true}},
		{name: "helpers", opts: ImportOptions{ExperimentalMacOS: true, InstallGuestHelpers: true}},
		{name: "conflicting arch", opts: ImportOptions{ExperimentalMacOS: true, Arch: "aarch64-linux"}},
		{name: "old guest", change: func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "restore.json"), []byte(`{"version":"26.0","build":"old"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}, opts: ImportOptions{ExperimentalMacOS: true}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			st, _ := newTestStore(t)
			dir := macOSBundle(t)
			if tt.change != nil {
				tt.change(t, dir)
			}
			opts := tt.opts
			opts.Name = "bad"
			opts.Src = dir
			if _, err := st.ImportImage(context.Background(), opts); err == nil {
				t.Fatal("invalid bundle accepted")
			}
			images, err := st.ListImages()
			if err != nil || len(images) != 0 {
				t.Fatalf("failed import registered: %v %v", images, err)
			}
		})
	}
}

func TestCancelledMacOSImportLeavesNoImage(t *testing.T) {
	st, _ := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := st.ImportImage(ctx, ImportOptions{Name: "cancelled", Src: macOSBundle(t), ExperimentalMacOS: true}); err == nil {
		t.Fatal("cancelled import succeeded")
	}
	entries, err := os.ReadDir(filepath.Dir(st.ImageDir("unused")))
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled import left artifacts: %v %v", entries, err)
	}
}

func TestMacOSStorageValidationDetectsMissingAndReplacedPlatformState(t *testing.T) {
	st, _ := newTestStore(t)
	im, err := st.ImportImage(context.Background(), ImportOptions{Name: "mac", Src: macOSBundle(t), ExperimentalMacOS: true})
	if err != nil {
		t.Fatal(err)
	}
	dir := st.ImageDir(im.ID)
	if err := im.MacOS.ValidateStorage(dir); err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(dir, "MachineIdentifier")
	if err := os.WriteFile(identity, []byte("different identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := im.MacOS.ValidateStorage(dir); err == nil || !strings.Contains(err.Error(), "recorded Apple identity") {
		t.Fatalf("replaced identity accepted: %v", err)
	}
	if err := os.WriteFile(identity, []byte("MachineIdentifier"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "HardwareModel")); err != nil {
		t.Fatal(err)
	}
	if err := im.MacOS.ValidateStorage(dir); err == nil || !strings.Contains(err.Error(), "HardwareModel") {
		t.Fatalf("missing hardware model accepted: %v", err)
	}
}

func TestMacOSStorageLocksBlockImportAndHonorCancellation(t *testing.T) {
	if path := os.Getenv("VOOM_TEST_MACOS_LOCK"); path != "" {
		file, err := os.OpenFile(path, os.O_RDWR, 0)
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
	st, _ := newTestStore(t)
	src := macOSBundle(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMacOSStorageLocksBlockImportAndHonorCancellation$")
	cmd.Env = append(os.Environ(), "VOOM_TEST_MACOS_LOCK="+filepath.Join(src, "AuxiliaryStorage"))
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
		t.Fatalf("lock holder startup: %q %v", ready, err)
	}
	if _, err := st.ImportImage(context.Background(), ImportOptions{Name: "busy", Src: src, ExperimentalMacOS: true}); err == nil || !strings.Contains(err.Error(), "storage is in use") {
		t.Fatalf("import accepted a running source: %v", err)
	}
	destination := filepath.Join(t.TempDir(), "busy-probe")
	if _, err := PrepareMacOSCloneProbe(ctx, src, destination, false); err == nil {
		t.Fatal("probe accepted a locked source")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("locked probe left files: %v", err)
	}
	waitCtx, stop := context.WithTimeout(ctx, 30*time.Millisecond)
	defer stop()
	if err := WaitMacOSStorage(waitCtx, src); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("storage wait ignored cancellation: %v", err)
	}
	release()
	if err := WaitMacOSStorage(ctx, src); err != nil {
		t.Fatalf("released storage remained locked: %v", err)
	}
}
