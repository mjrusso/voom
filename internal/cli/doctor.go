package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mjrusso/voom/internal/doctor"
)

func doctorCommand() *cobra.Command {
	var all bool
	cmd := &cobra.Command{Use: "doctor", Short: "Run diagnostics", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		checks := doctor.Run(all)
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(checks)
		}
		for _, c := range checks {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", c.Severity, c.Name, c.Message)
		}
		return nil
	}}
	cmd.Flags().BoolVar(&all, "all", false, "include optional integration checks")
	return cmd
}
