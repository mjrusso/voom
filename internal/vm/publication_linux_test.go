package vm

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/mjrusso/voom/internal/atomicfile"
	"github.com/mjrusso/voom/internal/host"
	"github.com/mjrusso/voom/internal/state"
)

func TestPublicationFailurePreservesReferencedStorage(t *testing.T) {
	if action := os.Getenv("VOOM_PUBLICATION_HELPER"); action != "" {
		// strace counts syscalls per thread, so migration would change the injected boundary.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		st, err := state.Open(state.WithHostPortAvailable(func(string, int) (bool, string) { return true, "" }))
		if err != nil {
			t.Fatal(err)
		}
		switch action {
		case "import":
			_, err = st.ImportImage(context.Background(), state.ImportOptions{Name: "target", Src: os.Getenv("VOOM_PUBLICATION_SOURCE"), Arch: host.System(), Format: "qcow2", SSHUser: "root"})
		case "create":
			_, err = New(st).Create(context.Background(), "target", "source", "qemu", 2, 512, 0)
		case "clone":
			_, err = New(st).Clone(context.Background(), "source", "target")
		case "rename":
			_, err = st.RenameVM(context.Background(), "source", "target")
		case "import macos":
			_, err = st.ImportImage(context.Background(), state.ImportOptions{Name: "target", Src: os.Getenv("VOOM_PUBLICATION_SOURCE"), SSHUser: "root"})
		}
		stage := os.Getenv("VOOM_PUBLICATION_STAGE")
		if stage == "baseline" {
			if err != nil {
				t.Fatal(err)
			}
			return
		}
		if !errors.Is(err, syscall.EIO) {
			t.Fatalf("expected injected fsync error, got %v", err)
		}
		if atomicfile.Published(err) != (stage != "index file") {
			t.Fatalf("incorrect publication outcome for %s: %v", stage, err)
		}
		if stage == "index directory" {
			var published *atomicfile.PublicationError
			if !errors.As(err, &published) || published.Path != filepath.Join(st.Paths().State, "state.json") {
				t.Fatalf("expected index publication error: %v", err)
			}
		}
		index, readErr := state.ReadIndex(st.Paths().State)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if stage != "index directory" {
			if index.Images["target"] != "" || index.VMs["target"] != "" || st.IndexSnapshot().Images["target"] != "" || st.IndexSnapshot().VMs["target"] != "" {
				t.Fatalf("failed publication registered target: %+v", index)
			}
			objects, want := "vms", 0
			if strings.HasPrefix(action, "import") {
				objects, want = "images", 1
			}
			if action == "clone" || action == "rename" {
				want = 1
			}
			entries, err := os.ReadDir(filepath.Join(st.Paths().State, objects))
			if err != nil || len(entries) != want {
				t.Fatalf("staged objects remain: %v, %v", entries, err)
			}
			if action == "rename" {
				if _, err := st.LoadVM("source"); err != nil {
					t.Fatalf("failed rename damaged source: %v", err)
				}
			}
			return
		}
		reopened, openErr := state.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		if strings.HasPrefix(action, "import") {
			if index.Images["target"] == "" {
				t.Fatal("fault did not reach published index")
			}
			im, err := st.LoadImage("target")
			if err != nil {
				t.Fatalf("published image is unavailable: %v", err)
			}
			if _, err := os.Stat(st.ImageDiskPath(im)); err != nil {
				t.Fatalf("published image lost disk: %v", err)
			}
			if _, err := reopened.LoadImage("target"); err != nil {
				t.Fatalf("reopened image is unavailable: %v", err)
			}
		} else {
			if index.VMs["target"] == "" {
				t.Fatal("fault did not reach published index")
			}
			vm, err := st.LoadVM("target")
			if err != nil {
				t.Fatalf("published VM is unavailable: %v", err)
			}
			if _, err := os.Stat(st.VMDiskPath(vm)); err != nil {
				t.Fatalf("published VM lost disk: %v", err)
			}
			if _, err := reopened.LoadVM("target"); err != nil {
				t.Fatalf("reopened VM is unavailable: %v", err)
			}
		}
		return
	}
	strace, err := exec.LookPath("strace")
	if err != nil {
		t.Skip("strace is required for fsync fault injection")
	}
	for _, action := range []string{"import", "import macos", "create", "clone", "rename"} {
		t.Run(action, func(t *testing.T) {
			var phases map[string]int
			t.Run("baseline", func(t *testing.T) {
				dir, src := publicationFixture(t, action)
				trace := runPublication(t, strace, dir, src, action, "baseline", 0)
				phases = publicationPhases(t, trace)
			})
			for _, stage := range []string{"index directory", "index file", "record directory"} {
				if action == "rename" && stage == "record directory" {
					continue
				}
				t.Run(stage, func(t *testing.T) {
					ordinal, found := phases[stage]
					if !found {
						t.Fatalf("baseline did not publish %s", stage)
					}
					dir, src := publicationFixture(t, action)
					runPublication(t, strace, dir, src, action, stage, ordinal)
				})
			}
		})
	}
}

func publicationFixture(t *testing.T, action string) (string, string) {
	t.Helper()
	st, dir := newTestStore(t, state.WithHostPortAvailable(func(string, int) (bool, string) { return true, "" }))
	src := filepath.Join(dir, "source.qcow2")
	if err := os.WriteFile(src, []byte("guest disk"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ImportImage(context.Background(), state.ImportOptions{Name: "source", Src: src, Arch: host.System(), Format: "qcow2", SSHUser: "root"}); err != nil {
		t.Fatal(err)
	}
	if action == "clone" || action == "rename" {
		if _, err := New(st).Create(context.Background(), "source", "source", "qemu", 2, 512, 0); err != nil {
			t.Fatal(err)
		}
	}
	if action == "import macos" {
		src = filepath.Join(dir, "macos-bundle")
		if err := os.Mkdir(src, 0700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"image.json", "restore.json", "toolchain.json", "disk.raw", "MachineIdentifier", "HardwareModel", "AuxiliaryStorage"} {
			data := []byte(name)
			if strings.HasSuffix(name, ".json") {
				var err error
				data, err = os.ReadFile(filepath.Join("..", "..", "testdata", "macos-bundle", name))
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(src, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dir, src
}

func runPublication(t *testing.T, strace, dir, src, action, stage string, ordinal int) string {
	t.Helper()
	trace := filepath.Join(dir, "fsync.trace")
	args := []string{"-f", "-yy", "-o", trace, "-e", "trace=fsync"}
	if ordinal != 0 {
		args = append(args, "-e", "inject=fsync:error=EIO:when="+strconv.Itoa(ordinal))
	}
	args = append(args, os.Args[0], "-test.run=^TestPublicationFailurePreservesReferencedStorage$")
	cmd := exec.Command(strace, args...)
	cmd.Env = append(os.Environ(), "VOOM_PUBLICATION_HELPER="+action, "VOOM_PUBLICATION_SOURCE="+src, "VOOM_PUBLICATION_STAGE="+stage)
	out, err := cmd.CombinedOutput()
	data, readErr := os.ReadFile(trace)
	if err != nil || readErr != nil {
		t.Fatalf("publication %s: %v, trace: %v\n%s\n%s", stage, err, readErr, out, data)
	}
	return string(data)
}

func publicationPhases(t *testing.T, trace string) map[string]int {
	t.Helper()
	phases := make(map[string]int)
	counts := make(map[string]int)
	type directoryPhase struct{ path, name string }
	pending := make(map[string]directoryPhase)
	for _, match := range regexp.MustCompile(`(?m)^(\d+)\s+fsync\(\d+<([^>]+)>`).FindAllStringSubmatch(trace, -1) {
		tid, path := match[1], match[2]
		counts[tid]++
		if phase, found := pending[tid]; found && path == phase.path {
			phases[phase.name] = counts[tid]
			delete(pending, tid)
		}
		name := filepath.Base(path)
		switch {
		case strings.HasPrefix(name, ".state.json.tmp-"):
			phases["index file"] = counts[tid]
			pending[tid] = directoryPhase{filepath.Dir(path), "index directory"}
		case strings.HasPrefix(name, ".vm.json.tmp-"), strings.HasPrefix(name, ".image.json.tmp-"):
			pending[tid] = directoryPhase{filepath.Dir(path), "record directory"}
		}
	}
	return phases
}
