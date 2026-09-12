package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mjrusso/voom/internal/vm"
)

func egressCommand() *cobra.Command {
	root := &cobra.Command{Use: "egress", Short: "Configure an explicit proxy attachment", Long: "Configure a VM-private explicit proxy attachment. Enable and disable control access to the proxy; ordinary direct network egress remains available."}
	for _, action := range []string{"set", "clear", "enable", "disable"} {
		var socket, ca string
		cmd := &cobra.Command{Use: action + " <name>", Short: strings.ToUpper(action[:1]) + action[1:] + " the explicit proxy attachment", Args: cobra.ExactArgs(1)}
		switch action {
		case "set":
			cmd.Long = "Attach a unique host Unix proxy socket to a stopped VM and enable the attachment. An optional CA file must contain public certificates only."
		case "clear":
			cmd.Long = "Remove a stopped VM's proxy attachment and release its backend socket reservation."
		case "enable":
			cmd.Long = "Validate and enable the configured proxy attachment. A running VM does not need to restart."
		case "disable":
			cmd.Long = "Disable the proxy attachment and close its active tunnels. Keep its socket reservation for later re-enabling."
		}
		cmd.Long += " Ordinary direct network egress remains available."
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			deps, err := loadRuntimeDeps()
			if err != nil {
				return err
			}
			var result vm.EgressResult
			switch action {
			case "set":
				result, err = deps.vm.SetEgress(cmd.Context(), args[0], socket, ca)
			case "clear":
				result, err = deps.vm.ClearEgress(cmd.Context(), args[0])
			case "enable":
				result, err = deps.vm.EnableEgress(cmd.Context(), args[0])
			case "disable":
				result, err = deps.vm.DisableEgress(cmd.Context(), args[0])
			}
			if err != nil {
				return err
			}
			if outputFormat(cmd) == "json" {
				if action == "set" || action == "clear" {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": result.Name, "changed": result.Changed, "egress": result.Egress, "runtimeChanged": result.RuntimeChanged})
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": result.Name, "changed": result.Changed, "enabled": result.Enabled, "runtimeChanged": result.RuntimeChanged})
			}
			status := "unchanged"
			if result.Changed {
				status = "changed"
			} else if result.RuntimeChanged {
				status = "runtime repaired"
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "VM %s egress %s: %s; direct egress remains available\n", result.Name, action, status)
			return err
		}
		if action == "set" {
			cmd.Flags().StringVar(&socket, "backend-socket", "", "absolute path to this VM's host Unix proxy socket")
			cmd.Flags().StringVar(&ca, "ca-cert", "", "public CA certificate PEM file")
			_ = cmd.MarkFlagRequired("backend-socket")
		}
		root.AddCommand(cmd)
	}
	return root
}
