package state

import (
	"path/filepath"
	"testing"
)

// setTestEnv points VOOM_* directories at subdirs of dir so a test gets an
// isolated state/config/cache/runtime layout.
func setTestEnv(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("VOOM_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("VOOM_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("VOOM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("VOOM_RUNTIME_DIR", filepath.Join(dir, "runtime"))
}

// newTestStore creates a temp directory, points VOOM_* at it, and returns an
// opened Store plus the temp directory path.
func newTestStore(t *testing.T, opts ...Option) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	setTestEnv(t, dir)
	st, err := Open(opts...)
	if err != nil {
		t.Fatal(err)
	}
	return st, dir
}
