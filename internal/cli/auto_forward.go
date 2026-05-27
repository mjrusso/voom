package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func setAutoForward(cmd *cobra.Command, name string, enabled bool, offset int, setOffset bool) error {
	deps, err := loadRuntimeDeps()
	if err != nil {
		return err
	}
	vmRec, err := deps.vm.SetAutoForward(cmd.Context(), name, enabled, offset, setOffset)
	if err != nil {
		return err
	}
	if outputFormat(cmd) == "json" {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
			"name":        vmRec.Name,
			"changed":     true,
			"autoForward": vmRec.Network.AutoForward,
			"offset":      vmRec.Network.AutoForwardHostOffset,
		})
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "auto-forward %s for %s\n", map[bool]string{true: "enabled", false: "disabled"}[enabled], vmRec.Name)
	return nil
}

func setAutoForwardOffset(cmd *cobra.Command, name string, offset int) error {
	deps, err := loadRuntimeDeps()
	if err != nil {
		return err
	}
	vmRec, err := deps.store.LoadVM(name)
	if err != nil {
		return err
	}
	return setAutoForward(cmd, vmRec.Name, vmRec.Network.AutoForward, offset, true)
}
