//go:build darwin

package state

import (
	"context"

	"golang.org/x/sys/unix"
)

func tryCloneCopy(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return unix.Clonefile(src, dst, 0)
}
