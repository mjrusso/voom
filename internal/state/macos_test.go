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

func TestImportMacOSBundle(t *testing.T) {
	st, _ := newTestStore(t)
	src := macOSBundle(t)
	im, err := st.ImportImage(context.Background(), ImportOptions{Name: "macos", Src: src, SSHIdentityPath: "/keys/guest"})
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
}

func TestMacOSBundleRejectsInvalidInputsWithoutRegistration(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, string)
		opts   ImportOptions
	}{
		{name: "missing NVRAM", change: func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "AuxiliaryStorage")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink disk", change: func(t *testing.T, dir string) {
			if err := os.Rename(filepath.Join(dir, "disk.raw"), filepath.Join(dir, "other")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("other", filepath.Join(dir, "disk.raw")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "helpers", opts: ImportOptions{InstallGuestHelpers: true}},
		{name: "conflicting arch", opts: ImportOptions{Arch: "aarch64-linux"}},
		{name: "old guest", change: func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "restore.json"), []byte(`{"version":"26.0","build":"old"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
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
	if _, err := st.ImportImage(ctx, ImportOptions{Name: "cancelled", Src: macOSBundle(t)}); err == nil {
		t.Fatal("cancelled import succeeded")
	}
	entries, err := os.ReadDir(filepath.Dir(st.ImageDir("unused")))
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled import left artifacts: %v %v", entries, err)
	}
}

func TestMacOSStorageValidationDetectsMissingAndReplacedPlatformState(t *testing.T) {
	st, _ := newTestStore(t)
	im, err := st.ImportImage(context.Background(), ImportOptions{Name: "mac", Src: macOSBundle(t)})
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
	if _, err := st.ImportImage(context.Background(), ImportOptions{Name: "busy", Src: src}); err == nil || !strings.Contains(err.Error(), "storage is in use") {
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

func TestMacOSGenerationStateRejectsUnsafePathsAndOldSchemas(t *testing.T) {
	st, _ := newTestStore(t)
	im, err := st.ImportImage(context.Background(), ImportOptions{Name: "mac", Src: macOSBundle(t)})
	if err != nil {
		t.Fatal(err)
	}
	vm := &VMRecord{SchemaVersion: 2, ID: "mac", Name: "mac", Arch: im.Arch, Driver: "vfkit", MacOS: im.MacOS, Resources: VMResources{CPUs: 4, MemoryMiB: 8192}, StorageGeneration: strings.Repeat("a", 32)}
	if err := st.SaveVM(vm); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(vm.Name, vm.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadVM(vm.Name); err != nil {
		t.Fatal(err)
	}
	for _, generation := range []string{"../outside", "/tmp/outside", "", "not-a-generation"} {
		invalid := *vm
		invalid.StorageGeneration = generation
		if err := st.SaveVM(&invalid); err == nil {
			t.Fatalf("invalid generation saved: %q", generation)
		}
		if err := WriteJSONAtomic(filepath.Join(st.VMDir(vm.ID), "vm.json"), &invalid); err != nil {
			t.Fatal(err)
		}
		if _, err := st.LoadVM(vm.Name); err == nil {
			t.Fatalf("unsafe generation accepted: %q", generation)
		}
		if _, err := st.LoadVMByID(vm.ID); err == nil {
			t.Fatalf("unsafe generation accepted by ID: %q", generation)
		}
	}
	vm.SchemaVersion = 3
	if err := WriteJSONAtomic(filepath.Join(st.VMDir(vm.ID), "vm.json"), vm); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadVM(vm.Name); err == nil {
		t.Fatal("version-3 record accepted")
	}
}

func TestMacOSBundleGuestServiceCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name  string
		caps  ImageCapabilities
		valid bool
	}{
		{"legacy", ImageCapabilities{}, true},
		{"guest services", ImageCapabilities{ControlShare: true, GuestShareMount: true, GuestPortReport: true}, true},
		{"report without control", ImageCapabilities{GuestPortReport: true}, false},
		{"mount without control", ImageCapabilities{GuestShareMount: true}, false},
		{"Linux seed", ImageCapabilities{MetadataDisk: true}, false},
		{"Linux switch", ImageCapabilities{NixosSwitch: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := newTestStore(t)
			source := macOSBundle(t)
			path := filepath.Join(source, "image.json")
			var manifest map[string]any
			if err := ReadJSON(path, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest["capabilities"] = tc.caps
			if err := WriteJSONAtomic(path, manifest); err != nil {
				t.Fatal(err)
			}
			im, err := st.ImportImage(context.Background(), ImportOptions{Name: "mac", Src: source})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v import error=%v", tc.valid, err)
			}
			if !tc.valid {
				return
			}
			loaded, err := st.LoadImageByID(im.ID)
			if err != nil || loaded.Capabilities != tc.caps {
				t.Fatalf("capabilities lost: %+v %v", loaded, err)
			}
		})
	}
}

func TestMacOSRestoreResourceMinimums(t *testing.T) {
	for _, tc := range []struct {
		name         string
		restore      string
		valid        bool
		cpus, memory int
	}{
		{"legacy", `{"version":"27.0.1","build":"26A434"}`, true, 0, 0},
		{"Apple minimums", `{"version":"27.0.1","build":"26A434","minimumCPUCount":2,"minimumMemorySize":4294967297}`, true, 2, 4097},
		{"partial", `{"version":"27.0.1","build":"26A434","minimumCPUCount":2}`, false, 0, 0},
		{"zero", `{"version":"27.0.1","build":"26A434","minimumCPUCount":0,"minimumMemorySize":1}`, false, 0, 0},
		{"excessive CPUs", `{"version":"27.0.1","build":"26A434","minimumCPUCount":5,"minimumMemorySize":1}`, false, 0, 0},
		{"excessive memory", `{"version":"27.0.1","build":"26A434","minimumCPUCount":2,"minimumMemorySize":8589934593}`, false, 0, 0},
		{"overflow", `{"version":"27.0.1","build":"26A434","minimumCPUCount":2,"minimumMemorySize":18446744073709551615}`, false, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := newTestStore(t)
			source := macOSBundle(t)
			if err := os.WriteFile(filepath.Join(source, "restore.json"), []byte(tc.restore), 0o600); err != nil {
				t.Fatal(err)
			}
			image, err := st.ImportImage(context.Background(), ImportOptions{Name: "mac", Src: source})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v import error=%v", tc.valid, err)
			}
			if !tc.valid {
				return
			}
			loaded, err := st.LoadImageByID(image.ID)
			if err != nil {
				t.Fatal(err)
			}
			p := loaded.MacOS
			if p.MinimumCPUs != tc.cpus || p.MinimumMemoryMiB != tc.memory || p.CPUs != 4 || p.MemoryMiB != 8192 {
				t.Fatalf("resource metadata: %+v", p)
			}
			if tc.cpus == 0 {
				if err := p.ValidateResources(2, 4097); err == nil {
					t.Fatal("legacy floor was lowered")
				}
			} else {
				if err := p.ValidateResources(2, 4097); err != nil {
					t.Fatal(err)
				}
				if err := p.ValidateResources(1, 4097); err == nil {
					t.Fatal("CPU minimum ignored")
				}
				if err := p.ValidateResources(2, 4096); err == nil {
					t.Fatal("memory was rounded down")
				}
			}
		})
	}
}

func TestMacOSImportDerivesMetadataFromStagedBundle(t *testing.T) {
	for _, version := range []string{"27.9", "26.0"} {
		t.Run(version, func(t *testing.T) {
			st, _ := newTestStore(t)
			src := macOSBundle(t)
			originalCopy := cloneCopy
			t.Cleanup(func() { cloneCopy = originalCopy })
			cloneCopy = func(ctx context.Context, from, to string) error {
				if from == filepath.Join(src, "image.json") {
					var manifest map[string]any
					if err := ReadJSON(from, &manifest); err != nil {
						return err
					}
					manifest["user"] = "updated-user"
					manifest["cpuCount"] = 6
					manifest["capabilities"] = ImageCapabilities{ControlShare: true, GuestPortReport: true}
					if err := WriteJSONAtomic(from, manifest); err != nil {
						return err
					}
					if err := WriteJSONAtomic(filepath.Join(src, "restore.json"), map[string]string{"version": version, "build": "updated-build"}); err != nil {
						return err
					}
				}
				return originalCopy(ctx, from, to)
			}
			im, err := st.ImportImage(context.Background(), ImportOptions{Name: "changing", Src: src})
			if version == "26.0" {
				if err == nil {
					t.Fatal("invalid staged macOS version was registered")
				}
				entries, err := os.ReadDir(filepath.Dir(st.ImageDir("unused")))
				if err != nil || len(entries) != 0 || len(st.IndexSnapshot().Images) != 0 {
					t.Fatalf("invalid staged bundle left image state: %v %v", entries, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if im.MacOS.Version != version || im.MacOS.Build != "updated-build" || im.MacOS.CPUs != 6 || im.Metadata.SSHUser != "updated-user" || !im.Capabilities.GuestPortReport {
				t.Fatalf("registered metadata does not describe staged bundle: %+v %+v", im, im.MacOS)
			}
			var restore struct{ Version string }
			if err := ReadJSON(filepath.Join(st.ImageDir(im.ID), "restore.json"), &restore); err != nil || restore.Version != im.MacOS.Version {
				t.Fatalf("registered and copied versions differ: %+v %v", restore, err)
			}
		})
	}
}
