package control

import (
	"errors"
	"os"
)

// ReserveCID is unavailable without the Linux vhost-vsock kernel device.
func ReserveCID() (*os.File, uint32, error) { return nil, 0, errors.New("QEMU vsock requires Linux") }

func listenVSock(uint32) (acceptor, uint32, error) {
	return nil, 0, errors.New("QEMU vsock requires Linux")
}
