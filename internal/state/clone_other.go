//go:build !darwin

package state

import (
	"context"
	"os/exec"
)

func tryCloneCopy(ctx context.Context, src, dst string) error {
	return exec.CommandContext(ctx, "cp", "--reflink=auto", "--sparse=always", src, dst).Run()
}
