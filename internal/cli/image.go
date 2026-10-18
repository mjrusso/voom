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
	cmd.AddCommand(imageListCommand(), imageInspectCommand(), imageImportCommand(), imageRemoveCommand())
	return cmd
}

func imageListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List images",
		Args:    cobra.NoArgs,
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
			tw := tableWriter(cmd)
			for _, im := range images {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", im.Name, im.ID, im.Arch, im.Format)
			}
			return tw.Flush()
		},
	}
}

func imageInspectCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "inspect <name>",
		Aliases:           []string{"info"},
		ValidArgsFunction: completeImageNames,
		Short:             "Inspect an image",
		Args:              cobra.ExactArgs(1),
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
			if im.MacOS != nil {
				out["platformStateDir"] = deps.store.ImageDir(im.ID)
			}
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "name: %s\nid: %s\narch: %s\nformat: %s\ndisk: %s\ncreated: %s\nsource: %s\nsshUser: %s\nkernelPath: %s\ninitrdPath: %s\ninitPath: %s\nkernelCmdline: %s\ncapabilities: controlShare=%t guestPortReport=%t guestShareMount=%t nixosSwitch=%t\nreferencedBy: %s\n",
				im.Name, im.ID, im.Arch, im.Format, deps.store.ImageDiskPath(im), im.CreatedAt.Format(time.RFC3339), im.Metadata.Source, im.Metadata.SSHUser,
				im.Metadata.KernelPath, im.Metadata.InitrdPath, im.Metadata.InitPath, strings.Join(im.Metadata.KernelCmdline, " "),
				im.Capabilities.ControlShare, im.Capabilities.GuestPortReport, im.Capabilities.GuestShareMount, im.Capabilities.NixosSwitch, strings.Join(refs, ","))
			if im.MacOS != nil {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "system: %s\nboot: macos\nimage-macos: %s (%s)\nbaked-resources: %d CPUs, %dMiB\napple-identity: %s\nplatform-state: %s\n", im.Arch, im.MacOS.Version, im.MacOS.Build, im.MacOS.CPUs, im.MacOS.MemoryMiB, im.MacOS.IdentitySHA256, deps.store.ImageDir(im.ID))
				if im.MacOS.MinimumCPUs != 0 {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "apple-minimum-resources: %d CPUs, %dMiB\n", im.MacOS.MinimumCPUs, im.MacOS.MinimumMemoryMiB)
				}
			}
			return nil
		},
	}
}

func imageImportCommand() *cobra.Command {
	var metaPath, arch, format, sshUser, sshIdentity string
	var installGuestHelpers bool
	cmd := &cobra.Command{
		Use:               "import <name> <path>",
		ValidArgsFunction: completeImageImport,
		Short:             "Import an image",
		Args:              cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := state.ValidateName("image", args[0]); err != nil {
				return err
			}
			deps, err := loadRuntimeDeps()
			if err != nil {
				return err
			}
			im, err := deps.store.ImportImage(cmd.Context(), state.ImportOptions{
				Name:                args[0],
				SSHIdentityPath:     sshIdentity,
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
	cmd.Flags().StringVar(&format, "format", "", "image format: qcow2, raw, or macos-bundle")
	cmd.Flags().StringVar(&sshUser, "ssh-user", "", "SSH login user (overrides sidecar user)")
	cmd.Flags().BoolVar(&installGuestHelpers, "install-guest-helpers", false, "install voom guest helpers via cloud-init (enables shares + auto-discover)")
	cmd.Flags().StringVar(&sshIdentity, "ssh-identity", "", "path to the existing private key authorized in the guest")
	return cmd
}

func imageRemoveCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:               "remove <name>",
		ValidArgsFunction: completeImageNames,
		Aliases:           []string{"rm", "delete"},
		Short:             "Remove an image",
		Args:              cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := loadRuntimeDeps()
			if err != nil {
				return err
			}
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
