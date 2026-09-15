package vm

import (
	"bytes"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"

	"github.com/mjrusso/voom/internal/atomicfile"
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

func publishEgress(rt state.RuntimeLayout, ca []byte) (changed bool, err error) {
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
		if err = atomicfile.Write(rt.EgressCA(), ca, 0o644); err != nil {
			return changed, err
		}
	}
	err = atomicfile.Write(rt.EgressManifest(), manifest, 0o644)
	return changed, err
}
