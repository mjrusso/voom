package atomicfile

import (
	"errors"
	"fmt"
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
	if err := Write(path, []byte("data"), 0o644); err == nil || Published(err) {
		t.Fatalf("expected unpublished rename failure, got %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "destination" {
		t.Fatalf("unexpected files after failure: %v", entries)
	}
}

func TestWriteReportsPublicationWhenDirectoryCannotBeOpened(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := Write(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	if d, err := os.Open(dir); err == nil {
		_ = d.Close()
		t.Skip("directory permissions are not enforced for this user")
	}
	err := Write(path, []byte("replacement"), 0600)
	if err == nil || !Published(err) || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected published permission failure, got %v", err)
	}
	if !Published(errors.Join(fmt.Errorf("saving index: %w", err), errors.New("cleanup failed"))) {
		t.Fatal("wrapping lost publication outcome")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "replacement" {
		t.Fatalf("replacement is not visible: %q, %v", data, readErr)
	}
}
