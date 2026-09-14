package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mjrusso/voom/internal/state"
	"github.com/mjrusso/voom/internal/vm"
)

func updateAutoForward(cmd *cobra.Command, name string, update vm.AutoForwardUpdate) error {
	deps, err := loadRuntimeDeps()
	if err != nil {
		return err
	}
	vmRec, err := deps.vm.UpdateAutoForward(cmd.Context(), name, update)
	if err != nil {
		return err
	}
	return writeAutoForwardResult(cmd, vmRec)
}

func writeAutoForwardResult(cmd *cobra.Command, vmRec *state.VMRecord) error {
	autoBind := vmRec.Network.AutoForwardBind
	if autoBind == "" {
		autoBind = "127.0.0.1"
	}
	if outputFormat(cmd) == "json" {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
			"name":        vmRec.Name,
			"changed":     true,
			"autoForward": vmRec.Network.AutoForward,
			"offset":      vmRec.Network.AutoForwardHostOffset,
			"bind":        autoBind,
		})
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "auto-forward %s for %s (bind %s)\n", map[bool]string{true: "enabled", false: "disabled"}[vmRec.Network.AutoForward], vmRec.Name, autoBind)
	return nil
}
