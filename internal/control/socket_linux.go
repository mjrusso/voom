package control

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// VHOST_VSOCK_SET_GUEST_CID is _IOW(0xaf, 0x60, __u64) in linux/vhost.h.
const vhostVsockSetGuestCID = 0x4008af60

// ReserveCID reserves a kernel-wide CID; pass the descriptor to QEMU without closing it between reservation and launch.
func ReserveCID() (*os.File, uint32, error) {
	f, err := os.OpenFile("/dev/vhost-vsock", os.O_RDWR, 0)
	if err != nil {
		return nil, 0, err
	}
	for range 100 {
		var b [4]byte
		if _, err = rand.Read(b[:]); err != nil {
			break
		}
		cid := uint64(binary.LittleEndian.Uint32(b[:])&0x7fffffff) + 3
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), vhostVsockSetGuestCID, uintptr(unsafe.Pointer(&cid)))
		if errno == 0 {
			return f, uint32(cid), nil
		}
		err = errno
		if !errors.Is(err, unix.EADDRINUSE) {
			break
		}
	}
	_ = f.Close()
	return nil, 0, fmt.Errorf("reserve vsock CID: %w", err)
}

type vsockListener struct {
	fd  int
	cid uint32
}

func listenVSock(cid uint32) (acceptor, uint32, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return nil, 0, err
	}
	fail := func(err error) (acceptor, uint32, error) { _ = unix.Close(fd); return nil, 0, err }
	if err = unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: unix.VMADDR_PORT_ANY}); err != nil {
		return fail(err)
	}
	if err = unix.Listen(fd, 8); err != nil {
		return fail(err)
	}
	addr, err := unix.Getsockname(fd)
	if err != nil {
		return fail(err)
	}
	return &vsockListener{fd, cid}, addr.(*unix.SockaddrVM).Port, nil
}

func (l *vsockListener) Close() error { return unix.Close(l.fd) }

func (l *vsockListener) Accept() (Stream, error) {
	p := []unix.PollFd{{Fd: int32(l.fd), Events: unix.POLLIN}}
	n, err := unix.Poll(p, 500)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, os.ErrDeadlineExceeded
	}
	fd, addr, err := unix.Accept4(l.fd, unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK)
	if err != nil {
		return nil, err
	}
	peer, ok := addr.(*unix.SockaddrVM)
	if !ok || peer.CID != l.cid {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("unexpected vsock peer: %v", addr)
	}
	f := os.NewFile(uintptr(fd), "vsock")
	if err := f.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
