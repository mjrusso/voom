package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mjrusso/voom/internal/egress"
	"github.com/mjrusso/voom/internal/state"
	"github.com/mjrusso/voom/internal/vm"
)

func egressCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "egress", Short: "Configure an explicit proxy attachment", Long: "Configure a VM-private explicit proxy attachment. Enable and disable control access to the proxy; ordinary direct network egress remains available."}
	cmd.AddCommand(egressSetCommand(), egressClearCommand(), egressEnableCommand(), egressDisableCommand())
	return cmd
}

func egressSetCommand() *cobra.Command {
	var socket, ca, expectedID string
	var disabled bool
	cmd := &cobra.Command{
		Use:   "set <name>",
		Short: "Set the explicit proxy attachment",
		Long:  "Attach a unique host Unix proxy socket to a stopped VM. The attachment is enabled unless --disabled is set. An optional CA file must contain public certificates only. Ordinary direct network egress remains available.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := loadRuntimeDeps()
			if err != nil {
				return err
			}
			decl, err := egress.Normalize(socket, ca)
			if err != nil {
				return err
			}
			decl.Enabled = !disabled
			result, err := deps.vm.SetEgress(cmd.Context(), args[0], decl, vm.EgressOptions{ExpectedID: expectedID})
			if err != nil {
				return err
			}
			return writeEgressResult(cmd, "set", result)
		},
	}
	cmd.Flags().StringVar(&socket, "backend-socket", "", "absolute path to this VM's host Unix proxy socket")
	cmd.Flags().StringVar(&ca, "ca-cert", "", "public CA certificate PEM file")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "store the attachment disabled")
	cmd.Flags().StringVar(&expectedID, "expect-id", "", "require the VM to have this immutable ID")
	_ = cmd.MarkFlagRequired("backend-socket")
	return cmd
}

func egressClearCommand() *cobra.Command {
	var expectedID string
	cmd := &cobra.Command{
		Use:   "clear <name>",
		Short: "Clear the explicit proxy attachment",
		Long:  "Remove a stopped VM's proxy attachment and release its backend socket reservation. Ordinary direct network egress remains available.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := loadRuntimeDeps()
			if err != nil {
				return err
			}
			result, err := deps.vm.ClearEgress(cmd.Context(), args[0], vm.EgressOptions{ExpectedID: expectedID})
			if err != nil {
				return err
			}
			return writeEgressResult(cmd, "clear", result)
		},
	}
	cmd.Flags().StringVar(&expectedID, "expect-id", "", "require the VM to have this immutable ID")
	return cmd
}

func egressEnableCommand() *cobra.Command {
	var expectedID string
	cmd := &cobra.Command{
		Use:   "enable <name>",
		Short: "Enable the explicit proxy attachment",
		Long:  "Validate and enable the configured proxy attachment. A running VM does not need to restart. Ordinary direct network egress remains available.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := loadRuntimeDeps()
			if err != nil {
				return err
			}
			result, err := deps.vm.EnableEgress(cmd.Context(), args[0], vm.EgressOptions{ExpectedID: expectedID})
			if err != nil {
				return err
			}
			return writeEgressResult(cmd, "enable", result)
		},
	}
	cmd.Flags().StringVar(&expectedID, "expect-id", "", "require the VM to have this immutable ID")
	return cmd
}

func egressDisableCommand() *cobra.Command {
	var expectedID string
	cmd := &cobra.Command{
		Use:   "disable <name>",
		Short: "Disable the explicit proxy attachment",
		Long:  "Disable the proxy attachment and close its active tunnels. Keep its socket reservation for later re-enabling. Ordinary direct network egress remains available.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := loadRuntimeDeps()
			if err != nil {
				return err
			}
			result, err := deps.vm.DisableEgress(cmd.Context(), args[0], vm.EgressOptions{ExpectedID: expectedID})
			if err != nil {
				return err
			}
			return writeEgressResult(cmd, "disable", result)
		},
	}
	cmd.Flags().StringVar(&expectedID, "expect-id", "", "require the VM to have this immutable ID")
	return cmd
}

func writeEgressResult(cmd *cobra.Command, action string, result vm.EgressResult) error {
	if outputFormat(cmd) == "json" {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	status := "unchanged"
	if result.Changed {
		status = "changed"
	} else if result.RuntimeChanged {
		status = "runtime repaired"
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "VM %s egress %s: %s; direct egress remains available\n", result.Name, action, status)
	return err
}

func egressReplayCommand(record *state.VMRecord) string {
	d := record.Network.Egress
	if d == nil {
		return ""
	}
	line := "voom config egress set " + shellQuote(record.Name) + " --backend-socket " + shellQuote(d.BackendSocket)
	if d.CACertPath != "" {
		line += " --ca-cert " + shellQuote(d.CACertPath)
	}
	if !d.Enabled {
		line += " --disabled"
	}
	return line
}

func egressStatus(d *egress.Decl, observed *vm.EgressRuntime) string {
	status := "not configured"
	if d != nil {
		status = "disabled while stopped"
		if d.Enabled {
			status = "enabled for next start"
		}
	}
	if observed != nil {
		status = "running, " + observed.State
		if observed.ActiveConnections != nil {
			status += fmt.Sprintf(", %d active connections", *observed.ActiveConnections)
		}
		if observed.Error != "" {
			status += "; " + observed.Error
		}
	}
	return status
}

func egressConfig(d *egress.Decl) string {
	switch {
	case d == nil:
		return "none"
	case d.Enabled:
		return "enabled"
	default:
		return "disabled"
	}
}
