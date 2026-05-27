package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mjrusso/voom/internal/state"
	"github.com/mjrusso/voom/internal/vm"
)

func nixosCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "nixos", Short: "NixOS integrations"}
	var flake string
	sw := &cobra.Command{Use: "switch <name> --flake <flake-ref> [nixos-rebuild args...]", Short: "Run nixos-rebuild switch", Args: cobra.MinimumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		if flake == "" {
			return errors.New("--flake is required")
		}
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		vmRec, err := deps.store.LoadVM(args[0])
		if err != nil {
			return err
		}
		unlock, err := deps.store.LockVM(vmRec.ID)
		if err != nil {
			return err
		}
		defer unlock()
		im, err := deps.store.LoadImageByID(vmRec.Image.ID)
		if err != nil {
			return err
		}
		if !im.Capabilities.NixosSwitch {
			return fmt.Errorf("NixOS switch unavailable for image %q: nixosSwitch capability is false; import an image built for voom nixos switch", im.Name)
		}
		if !deps.vm.IsRunning(vmRec) {
			return fmt.Errorf("VM %q is not running", vmRec.Name)
		}
		nargs := []string{"switch", "--flake", flake, "--target-host", vmRec.Access.NixosTargetUser + "@" + vmRec.Network.SSHBind}
		nargs = append(nargs, args[1:]...)
		nbCmd := exec.CommandContext(c.Context(), "nixos-rebuild", nargs...)
		nbCmd.Env = append(os.Environ(), "NIX_SSHOPTS="+strings.Join(vm.SSHBaseArgs(vmRec), " "))
		nbCmd.Stdin, nbCmd.Stdout, nbCmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := nbCmd.Run(); err != nil {
			return err
		}
		vmRec.Nixos = &state.NixOSSwitch{SwitchedAt: time.Now().UTC(), FlakeRef: flake, FlakeRev: gitRev(".")}
		vmRec.UpdatedAt = time.Now().UTC()
		return deps.store.SaveVM(vmRec)
	}}
	sw.Flags().StringVar(&flake, "flake", "", "flake reference")
	cmd.AddCommand(sw)
	return cmd
}

func gitRev(dir string) string {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
