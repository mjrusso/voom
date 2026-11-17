// Package atomicfile durably replaces files through same-directory temporary files.
package atomicfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// PublicationError means the replacement is visible but its crash durability is unconfirmed.
// Callers must preserve any objects the published file can reference.
type PublicationError struct {
	Path string
	Err  error
}

func (e *PublicationError) Error() string {
	return fmt.Sprintf("%s published but durability unconfirmed: %v", e.Path, e.Err)
}

func (e *PublicationError) Unwrap() error { return e.Err }

// Published reports whether a write succeeded or returned a PublicationError.
// It remains true when a PublicationError is wrapped or joined with other errors.
func Published(err error) bool {
	var published *PublicationError
	return err == nil || errors.As(err, &published)
}

// Write durably replaces path. A PublicationError indicates that rename succeeded;
// any other error leaves the destination unchanged.
func Write(path string, data []byte, perm fs.FileMode) (retErr error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	renamed := false
	defer func() {
		_ = f.Close()
		if !renamed {
			if err := os.Remove(f.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
				retErr = errors.Join(retErr, err)
			}
		}
	}()
	if err := f.Chmod(perm); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	renamed = true
	d, err := os.Open(dir)
	if err != nil {
		return &PublicationError{Path: path, Err: err}
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return &PublicationError{Path: path, Err: err}
	}
	return nil
}
