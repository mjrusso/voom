package cli

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

func guiCommand() *cobra.Command {
	return &cobra.Command{
		Use: "gui <name>", Short: "Show or focus a running MacOS VM's display",
		Long: "Show or focus the existing display of a running MacOS VM. Closing the window keeps the VM running. This command does not start or restart a VM.",
		Args: cobra.ExactArgs(1), ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := loadRuntimeDeps()
			if err != nil {
				return err
			}
			if err := deps.vm.ShowGUI(cmd.Context(), args[0]); err != nil {
				return err
			}
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": args[0], "display": "shown"})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "showing display for VM %s\n", args[0])
			return nil
		},
	}
}

func runtimeWatchCommand() *cobra.Command {
	return &cobra.Command{
		Use: "watch-vm <id> <driver-fingerprint>", Hidden: true, Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := loadRuntimeDeps()
			if err != nil {
				return err
			}
			return deps.vm.WatchRuntime(cmd.Context(), args[0], args[1])
		},
	}
}

func controlCommand() *cobra.Command {
	return &cobra.Command{Use: "control <id> <cid>", Hidden: true, Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		cid, err := strconv.ParseUint(args[1], 10, 32)
		if err != nil {
			return err
		}
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		return deps.vm.WatchControl(cmd.Context(), args[0], uint32(cid))
	}}
}
