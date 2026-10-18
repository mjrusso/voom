package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mjrusso/voom/internal/state"
)

func macOSCloneProbeCommand() *cobra.Command {
	var fresh bool
	cmd := &cobra.Command{
		Use:   "macos-clone-bundle <source-bundle> <destination>",
		Short: "Copy a macOS bundle for identity testing",
		Long:  "Copy a stopped macOS image bundle without importing or booting it. The destination must be new and its parent directory must exist. With --fresh-identity, use the packaged Apple identity helper to replace the copied machine identifier. The disk, hardware model, and auxiliary storage stay paired. Normal create, clone, and macOS disk reset always generate a fresh identifier.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := state.PrepareMacOSCloneProbe(cmd.Context(), args[0], args[1], fresh)
			if err != nil {
				return err
			}
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "prepared bundle: %s\nfresh Apple identity: %t\nsource identity: %s\ncopy identity: %s\nevidence: %s/identity-probe.json\n", report.Destination, report.FreshIdentity, report.SourceIdentitySHA256, report.CloneIdentitySHA256, report.Destination)
			return err
		},
	}
	cmd.Flags().BoolVar(&fresh, "fresh-identity", false, "generate a new Apple machine identifier (requires an Apple Silicon Mac and the packaged identity helper)")
	return cmd
}
