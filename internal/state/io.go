package state

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

var cloneCopy = tryCloneCopy

// ctxReader wraps an io.Reader and aborts the next Read when ctx is done.
// Cancel latency is bounded by the size of the buffer used by the surrounding
// io.Copy call (32 KiB by default).
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// WriteJSONAtomic writes v as indented JSON to path via a tempfile-and-rename plus fsync of the directory.
func WriteJSONAtomic(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp." + strconv.Itoa(os.Getpid())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// CopyFile copies src to dst, preferring a reflink/clone fast path and falling
// back to a streaming copy. The copy is aborted if ctx is cancelled; any
// partially-written .partial file is removed on every non-success path.
func CopyFile(ctx context.Context, src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".partial"
	finalized := false
	defer func() {
		if !finalized {
			_ = os.Remove(tmp)
		}
	}()
	if err := cloneCopy(ctx, src, tmp); err == nil {
		match, statErr := copiedFileSizeMatches(src, tmp)
		if statErr == nil && match {
			if err := finalizeCopiedFile(tmp, dst); err != nil {
				return err
			}
			finalized = true
			return nil
		}
		if statErr != nil {
			return statErr
		}
	}
	// A cancelled cloneCopy (SIGKILL'd cp on Linux, or short clonefile on
	// Darwin) must not fall through to the streaming retry.
	if err := ctx.Err(); err != nil {
		return err
	}
	_ = os.Remove(tmp)
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, &ctxReader{ctx: ctx, r: in}); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := finalizeCopiedFile(tmp, dst); err != nil {
		return err
	}
	finalized = true
	return nil
}

func finalizeCopiedFile(tmp, dst string) error {
	if err := os.Chmod(tmp, 0o644); err != nil {
		return err
	}
	if err := syncPath(tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	return syncDir(filepath.Dir(dst))
}

func copiedFileSizeMatches(src, dst string) (bool, error) {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return false, err
	}
	dstInfo, err := os.Stat(dst)
	if err != nil {
		return false, err
	}
	return srcInfo.Size() == dstInfo.Size(), nil
}

func syncPath(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

// ReadJSON reads the file at path and unmarshals its JSON contents into v.
func ReadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
