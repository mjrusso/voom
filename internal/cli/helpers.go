package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/mjrusso/voom/internal/host"
)

func helpersCommand() *cobra.Command {
	return &cobra.Command{
		Use: "helpers", Short: "Print resolved Voom and bundled helper paths", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			paths := map[string]string{"voom": executable}
			names := []string{"gvproxy"}
			if runtime.GOOS == "darwin" {
				names = append(names, "vfkit")
				if runtime.GOARCH == "arm64" {
					names = append(names, "voom-macos-identity")
				}
			}
			for _, name := range names {
				path, err := host.ExePath(name)
				if err != nil {
					return fmt.Errorf("resolve %s: %w", name, err)
				}
				paths[name] = path
			}
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(paths)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "voom: %s\n", executable)
			for _, name := range names {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", name, paths[name])
			}
			return nil
		},
	}
}
