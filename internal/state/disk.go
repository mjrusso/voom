package state

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"syscall"
)

// DiskUsage reports a disk's guest-visible capacity and the host blocks allocated to it.
type DiskUsage struct {
	VirtualBytes   int64 `json:"virtualBytes"`
	AllocatedBytes int64 `json:"allocatedBytes"`
}

// ReadDiskUsage reads capacity from a qcow2 header or a raw file's length, choosing by extension.
func ReadDiskUsage(path string) (DiskUsage, error) {
	f, err := os.Open(path)
	if err != nil {
		return DiskUsage{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return DiskUsage{}, err
	}
	usage := DiskUsage{VirtualBytes: info.Size()}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		usage.AllocatedBytes = st.Blocks * 512
	}
	switch InferFormat(path) {
	case "raw":
		return usage, nil
	case "qcow2":
		// The header stores the virtual size as a big-endian uint64 at offset 24.
		var header [32]byte
		if _, err := io.ReadFull(f, header[:]); err != nil {
			return DiskUsage{}, fmt.Errorf("reading qcow2 header: %w", err)
		}
		if string(header[:4]) != "QFI\xfb" {
			return DiskUsage{}, errors.New("disk is not a qcow2 image")
		}
		size := binary.BigEndian.Uint64(header[24:])
		if size > math.MaxInt64 {
			return DiskUsage{}, errors.New("qcow2 virtual size is out of range")
		}
		usage.VirtualBytes = int64(size)
		return usage, nil
	default:
		return DiskUsage{}, fmt.Errorf("unknown disk format for %s", path)
	}
}
