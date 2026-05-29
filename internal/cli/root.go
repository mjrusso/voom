// Package cli wires the cobra command tree exposed by the voom binary.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mjrusso/voom/internal/host"
	"github.com/mjrusso/voom/internal/vm"
)

type rootOptions struct {
	output  string
	verbose bool
}

// Execute parses os.Args, dispatches to the matching subcommand, and exits non-zero on failure.
func Execute() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := NewRootCommand().ExecuteContext(ctx); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// NewRootCommand assembles the full cobra command tree and returns the root.
func NewRootCommand() *cobra.Command {
	opts := &rootOptions{}
	cmd := &cobra.Command{
		Use:           "voom",
		Short:         "Manage local development VMs",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			switch opts.output {
			case "text", "json":
				return nil
			default:
				return fmt.Errorf("unsupported output format %q; use text or json", opts.output)
			}
		},
	}
	cmd.CompletionOptions.DisableDefaultCmd = true
	cmd.PersistentFlags().StringVar(&opts.output, "output", "text", "output format: text or json")
	cmd.PersistentFlags().BoolVarP(&opts.verbose, "verbose", "v", false, "enable verbose diagnostics")
	addCommands(cmd)
	return cmd
}

func outputFormat(cmd *cobra.Command) string {
	if s, err := cmd.Root().PersistentFlags().GetString("output"); err == nil && s != "" {
		return s
	}
	return "text"
}

// tableWriter returns a tabwriter that pads columns to a uniform width with a
// two-space gutter. Callers must Flush it before returning. Use it for any
// multi-row text table so wide cells don't push later columns out of line.
func tableWriter(cmd *cobra.Command) *tabwriter.Writer {
	return tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
}

func addCommands(root *cobra.Command) {
	root.AddCommand(newVersionCommand(), doctorCommand(), debugCommand(), guestCommand())
	root.AddCommand(imageCommand(), createCommand(), startCommand(), stopCommand(), restartCommand())
	root.AddCommand(sshCommand(), sshConfigCommand(), consoleCommand(), logsCommand(), infoCommand())
	root.AddCommand(listCommand(), renameCommand(), rmCommand(), diskCommand(), forwardCommand(), shareCommand(), nixosCommand())
}

func debugCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "debug", Short: "Debug voom internals"}
	cmd.AddCommand(&cobra.Command{
		Use:   "paths",
		Short: "Print resolved directories",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := host.ResolvePaths()
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(p)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "config: %s\nstate: %s\ncache: %s\nruntime: %s\n", p.Config, p.State, p.Cache, p.Runtime)
			return nil
		},
	})
	return cmd
}

func guestCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "guest", Short: "Inspect guest-reported state"}
	cmd.AddCommand(&cobra.Command{Use: "ports <name>", Short: "Show guest listeners", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		vmRec, err := deps.store.LoadVM(args[0])
		if err != nil {
			return err
		}
		im, err := deps.store.LoadImageByID(vmRec.Image.ID)
		if err != nil {
			return err
		}
		if err := vm.RequireGuestPortReport(im, "guest listener inspection"); err != nil {
			return err
		}
		report, err := deps.vm.ReadGuestPorts(vmRec)
		if err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
		}
		tw := tableWriter(cmd)
		for _, l := range report.Listeners {
			_, _ = fmt.Fprintf(tw, "%s\t%s:%d\t%s\n", l.Proto, l.Addr, l.Port, l.Process)
		}
		return tw.Flush()
	}})
	return cmd
}
