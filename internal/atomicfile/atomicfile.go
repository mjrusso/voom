// Package atomicfile durably replaces files through same-directory temporary files.
package atomicfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Write replaces path after the new contents and directory entry are durable.
func Write(path string, data []byte, perm fs.FileMode) (retErr error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
		if err := os.Remove(f.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			retErr = errors.Join(retErr, err)
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
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
