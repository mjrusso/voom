package vm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mjrusso/voom/internal/state"
)

// newTestStore uses a short runtime path so generated Unix socket paths remain valid.
func newTestStore(t *testing.T, opts ...state.Option) (*state.Store, string) {
	t.Helper()
	dir := t.TempDir()
	runtimeDir, err := os.MkdirTemp("/tmp", "voom-runtime-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	t.Setenv("VOOM_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("VOOM_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("VOOM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("VOOM_RUNTIME_DIR", runtimeDir)
	st, err := state.Open(opts...)
	if err != nil {
		t.Fatal(err)
	}
	return st, dir
}
