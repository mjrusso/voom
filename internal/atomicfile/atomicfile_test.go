package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReplacesFileAndSetsMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	if err := Write(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second" || info.Mode().Perm() != 0o644 {
		t.Fatalf("content=%q mode=%o", data, info.Mode().Perm())
	}
}

func TestWriteRemovesTemporaryFileAfterRenameFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "destination")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("data"), 0o644); err == nil {
		t.Fatal("expected rename failure")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "destination" {
		t.Fatalf("unexpected files after failure: %v", entries)
	}
}
