package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mjrusso/voom/internal/state"
)

func imageCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "image", Short: "Manage images"}
	cmd.AddCommand(imageListCommand(), imageInspectCommand(), imageImportCommand(), imageRmCommand())
	return cmd
}

func imageListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List images",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			deps, err := loadRuntimeDeps()
			if err != nil {
				return err
			}
			images, err := deps.store.ListImages()
			if err != nil {
				return err
			}
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(images)
			}
			for _, im := range images {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", im.Name, im.ID, im.Arch, im.Format)
			}
			return nil
		},
	}
}

func imageInspectCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <name>",
		Short: "Inspect an image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := loadRuntimeDeps()
			if err != nil {
				return err
			}
			im, err := deps.store.LoadImage(args[0])
			if err != nil {
				return err
			}
			vms, err := deps.store.ListVMs()
			if err != nil {
				return err
			}
			refs := []string{}
			for _, vmRec := range vms {
				if vmRec.Image.ID == im.ID {
					refs = append(refs, vmRec.Name)
				}
			}
			sort.Strings(refs)
			out := map[string]any{"image": im, "diskPath": deps.store.ImageDiskPath(im), "referencedBy": refs}
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "name: %s\nid: %s\narch: %s\nformat: %s\ndisk: %s\ncreated: %s\nsource: %s\nsshUser: %s\nkernelPath: %s\ninitrdPath: %s\ninitPath: %s\nkernelCmdline: %s\ncapabilities: controlShare=%t guestPortReport=%t guestShareMount=%t nixosSwitch=%t\nreferencedBy: %s\n",
				im.Name, im.ID, im.Arch, im.Format, deps.store.ImageDiskPath(im), im.CreatedAt.Format(time.RFC3339), im.Metadata.Source, im.Metadata.SSHUser,
				im.Metadata.KernelPath, im.Metadata.InitrdPath, im.Metadata.InitPath, strings.Join(im.Metadata.KernelCmdline, " "),
				im.Capabilities.ControlShare, im.Capabilities.GuestPortReport, im.Capabilities.GuestShareMount, im.Capabilities.NixosSwitch, strings.Join(refs, ","))
			return nil
		},
	}
}

func imageImportCommand() *cobra.Command {
	var metaPath, arch, format, sshUser string
	var installGuestHelpers bool
	cmd := &cobra.Command{
		Use:   "import <name> <path>",
		Short: "Import an image",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := state.ValidateName("image", args[0]); err != nil {
				return err
			}
			deps, err := loadRuntimeDeps()
			if err != nil {
				return err
			}
			unlock, err := deps.store.LockGlobal()
			if err != nil {
				return err
			}
			defer unlock()
			im, err := deps.store.ImportImage(cmd.Context(), state.ImportOptions{
				Name:                args[0],
				Src:                 args[1],
				MetaPath:            metaPath,
				Arch:                arch,
				Format:              format,
				SSHUser:             sshUser,
				InstallGuestHelpers: installGuestHelpers,
			})
			if err != nil {
				return err
			}
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": im.Name, "id": im.ID, "changed": true})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "imported image %s (%s)\n", im.Name, im.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&metaPath, "meta", "", "metadata sidecar path")
	cmd.Flags().StringVar(&arch, "arch", "", "image architecture/system")
	cmd.Flags().StringVar(&format, "format", "", "image format: qcow2 or raw")
	cmd.Flags().StringVar(&sshUser, "ssh-user", "", "SSH login user (overrides sidecar user)")
	cmd.Flags().BoolVar(&installGuestHelpers, "install-guest-helpers", false, "install voom guest helpers via cloud-init (enables shares + auto-discover)")
	return cmd
}

func imageRmCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove an image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := loadRuntimeDeps()
			if err != nil {
				return err
			}
			unlock, err := deps.store.LockGlobal()
			if err != nil {
				return err
			}
			defer unlock()
			refs, err := deps.store.RemoveImage(args[0], force)
			if err != nil {
				if len(refs) > 0 {
					return fmt.Errorf("%w: referenced by %s; pass --force to remove anyway", err, strings.Join(refs, ", "))
				}
				return err
			}
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": args[0], "changed": true})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "removed image %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "remove even if referenced")
	return cmd
}
