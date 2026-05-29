package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// Populated at build time via -ldflags -X.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// VersionInfo carries the build's version, commit, and date for display and JSON output.
type VersionInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// CurrentVersion returns the build's version metadata as set by -ldflags at build time.
func CurrentVersion() VersionInfo {
	return VersionInfo{Version: version, Commit: commit, Date: date}
}

// WriteVersion renders v to w as text or JSON depending on format.
func WriteVersion(w io.Writer, v VersionInfo, format string) error {
	if format == "json" {
		return json.NewEncoder(w).Encode(v)
	}
	_, err := fmt.Fprintf(w, "%s\n\nvoom %s\ncommit: %s\nbuilt: %s\n", banner, v.Version, v.Commit, v.Date)
	return err
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return WriteVersion(cmd.OutOrStdout(), CurrentVersion(), outputFormat(cmd))
		},
	}
}
