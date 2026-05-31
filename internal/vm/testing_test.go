package vm

import (
	"path/filepath"
	"testing"

	"github.com/mjrusso/voom/internal/state"
)

// newTestStore creates a temp directory, points VOOM_* env vars at it, and
// returns an opened state.Store plus the temp directory path.
func newTestStore(t *testing.T, opts ...state.Option) (*state.Store, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("VOOM_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("VOOM_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("VOOM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("VOOM_RUNTIME_DIR", filepath.Join(dir, "runtime"))
	st, err := state.Open(opts...)
	if err != nil {
		t.Fatal(err)
	}
	return st, dir
}
