package vm

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/mjrusso/voom/internal/egress"
	"github.com/mjrusso/voom/internal/state"
)

func egressFileMatches(path string, data []byte) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return data == nil, nil
	}
	if err != nil {
		return false, err
	}
	if data == nil || info.Mode() != 0644 {
		return false, nil
	}
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()
	info, err = file.Stat()
	if err != nil {
		return false, err
	}
	if info.Mode() != 0644 {
		return false, nil
	}
	got, err := io.ReadAll(io.LimitReader(file, int64(len(data))+1))
	return err == nil && bytes.Equal(got, data), err
}

func removeEgressFiles(rt state.RuntimeLayout) (bool, error) {
	changed := false
	var errs []error
	for _, path := range []string{rt.EgressManifest(), rt.EgressCA()} {
		err := os.Remove(path)
		if err == nil {
			changed = true
		} else if !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return changed, errors.Join(errs...)
}

func publishEgress(rt state.RuntimeLayout, enabled bool, ca []byte) (changed bool, err error) {
	if !enabled {
		return removeEgressFiles(rt)
	}
	manifest := egress.ManifestBytes(ca)
	manifestOK, err := egressFileMatches(rt.EgressManifest(), manifest)
	if err != nil {
		return false, err
	}
	caOK, err := egressFileMatches(rt.EgressCA(), ca)
	if err != nil {
		return false, err
	}
	if manifestOK && caOK {
		return false, nil
	}
	defer func() {
		if err != nil {
			removed, cleanup := removeEgressFiles(rt)
			changed = changed || removed
			err = errors.Join(err, cleanup)
		}
	}()
	if err = os.Remove(rt.EgressManifest()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	changed = true
	if len(ca) == 0 {
		if err = os.Remove(rt.EgressCA()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return changed, err
		}
	} else if !caOK {
		if err = writeEgressFile(rt.EgressCA(), ca); err != nil {
			return changed, err
		}
	}
	err = writeEgressFile(rt.EgressManifest(), manifest)
	return changed, err
}

func writeEgressFile(path string, data []byte) (err error) {
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".egress-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
		if e := os.Remove(f.Name()); e != nil && !errors.Is(e, os.ErrNotExist) {
			err = errors.Join(err, e)
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Chmod(0644); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("publish egress file: %w", err)
	}
	return nil
}
