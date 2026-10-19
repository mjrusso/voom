package control

import (
	"io"
	"net"
	"os"
	"time"
)

// Stream must support deadlines so an unresponsive guest cannot retain a session.
type Stream interface {
	io.ReadWriteCloser
	SetDeadline(time.Time) error
}

type acceptor interface {
	Accept() (Stream, error)
	Close() error
}

type unixListener struct{ *net.UnixListener }

func (l unixListener) Accept() (Stream, error) {
	if err := l.SetDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		return nil, err
	}
	return l.AcceptUnix()
}

// ListenHost returns a private Unix endpoint for vfkit or a CID-filtered vsock endpoint for QEMU.
func ListenHost(driver, path string, cid uint32) (acceptor, uint32, error) {
	if driver == "qemu" {
		return listenVSock(cid)
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, 0, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return nil, 0, err
	}
	return unixListener{l}, port, nil
}
