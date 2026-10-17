package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/mjrusso/voom/internal/state"
)

func macOSCloneProbeCommand() *cobra.Command {
	var fresh bool
	cmd := &cobra.Command{
		Use:   "macos-clone-bundle <source-bundle> <destination>",
		Short: "Prepare a disposable macOS bundle for identity experiments",
		Long:  "Copy a stopped builder bundle into a new directory whose parent already exists. Does not import or boot the copy. With --fresh-identity, use the packaged native helper to generate and read back a new Apple machine identifier, preserving the copied disk, hardware model, and auxiliary storage. Normal create and clone also assign fresh identities; reset remains unsupported.",
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
	cmd.Flags().BoolVar(&fresh, "fresh-identity", false, "generate a new Apple machine identifier (requires an Apple Silicon Mac and Swift)")
	return cmd
}

func macOSGuestCheckCommand() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use: "verify-macos <name>", ValidArgsFunction: completeVMNames,
		Short: "Collect macOS guest identity, boot, SSH, and Nix-daemon evidence",
		Long:  "Run read-only diagnostic commands over SSH against a running macOS guest. Retain stdout/stderr and check failures in JSON output. Does not start, stop, initialize, or change the guest's identity. A passing report does not establish clone/reset safety; compare reports from independent copies and repeated boots.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if timeout <= 0 {
				return fmt.Errorf("--timeout must be positive")
			}
			deps, err := loadRuntimeDeps()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			report, checkErr := deps.vm.CheckMacOSGuest(ctx, args[0])
			if report == nil {
				return checkErr
			}
			if outputFormat(cmd) == "json" {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
					return err
				}
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "VM: %s\nApple identity: %s\nMAC: %s\n", report.VM, report.MachineIdentifierSHA256, report.MAC)
				for _, check := range report.Checks {
					status := "ok"
					if !check.OK {
						status = "failed"
					}
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", check.Name, status)
					for _, detail := range []string{check.Stdout, check.Stderr, check.Error} {
						if detail != "" {
							_, _ = fmt.Fprintln(cmd.OutOrStdout(), detail)
						}
					}
				}
			}
			return checkErr
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 2*time.Minute, "total SSH diagnostic timeout")
	return cmd
}
