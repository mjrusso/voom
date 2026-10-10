//go:build !linux

package pci

import "errors"

func vfioGroupViable(string) (bool, error) {
	return false, errors.New("VFIO group checks are supported only on Linux")
}
