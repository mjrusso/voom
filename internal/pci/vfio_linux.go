//go:build linux

package pci

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	// Values from linux/vfio.h for VFIO_GROUP_GET_STATUS and VFIO_GROUP_FLAGS_VIABLE.
	vfioGroupGetStatus   = 0x3b67
	vfioGroupFlagsViable = 1
)

func vfioGroupViable(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	status := struct{ argsz, flags uint32 }{argsz: 8}
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), vfioGroupGetStatus, uintptr(unsafe.Pointer(&status))); errno != 0 {
		return false, errno
	}
	return status.flags&vfioGroupFlagsViable != 0, nil
}
