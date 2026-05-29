package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func shareCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "share", Short: "Manage shares"}
	var readonly bool
	add := &cobra.Command{Use: "add <name> <tag> <host-path> <guest-path>", Short: "Add a share", Args: cobra.ExactArgs(4), RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		decl, err := deps.vm.AddShare(args[0], args[1], args[2], args[3], readonly)
		if err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": args[0], "changed": true, "share": decl})
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", args[0], decl.Tag, decl.HostPath, decl.GuestPath)
		return nil
	}}
	add.Flags().BoolVar(&readonly, "readonly", false, "read-only (alias: --ro)")
	add.Flags().SetNormalizeFunc(func(_ *pflag.FlagSet, name string) pflag.NormalizedName {
		if name == "ro" {
			return "readonly"
		}
		return pflag.NormalizedName(name)
	})
	cmd.AddCommand(add)
	cmd.AddCommand(&cobra.Command{Use: "rm <name> <tag>", Short: "Remove a share", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		if err := deps.vm.RemoveShare(args[0], args[1]); err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": args[0], "tag": args[1], "changed": true})
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "removed share %s from %s\n", args[1], args[0])
		return nil
	}})
	cmd.AddCommand(&cobra.Command{Use: "ls <name>", Short: "List shares", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		vmRec, err := deps.store.LoadVM(args[0])
		if err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(vmRec.Shares)
		}
		tw := tableWriter(cmd)
		for _, s := range vmRec.Shares {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%t\n", s.Tag, s.HostPath, s.GuestPath, s.Readonly)
		}
		return tw.Flush()
	}})
	return cmd
}
