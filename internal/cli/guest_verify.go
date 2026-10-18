package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func guestVerifyCommand(macosOnly bool) *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use: "verify <name>", ValidArgsFunction: completeVMNames,
		Short: "Check guest health and configured services",
		Long:  "Check a running Linux or macOS guest over SSH. Verify guest health, mounts, and the services enabled by the image and VM configuration. Check Nix when detected; images with nixosSwitch require it. JSON output includes each check's output and errors. The command runs once without waiting for boot or changing the guest. Test restart, clone, reset, GUI, and forwarded traffic separately.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if timeout <= 0 {
				return fmt.Errorf("--timeout must be positive")
			}
			deps, err := loadRuntimeDeps()
			if err != nil {
				return err
			}
			if macosOnly {
				record, err := deps.store.LoadVM(args[0])
				if err != nil {
					return err
				}
				if record.MacOS == nil {
					return fmt.Errorf("verify-macos requires a macOS VM; use guest verify for Linux")
				}
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			report, checkErr := deps.vm.VerifyGuest(ctx, args[0])
			if report == nil {
				return checkErr
			}
			if outputFormat(cmd) == "json" {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
					return err
				}
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "VM: %s\nSystem: %s\nMAC: %s\n", report.VM, report.System, report.MAC)
				if report.MachineIdentifierSHA256 != "" {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Apple identity: %s\n", report.MachineIdentifierSHA256)
				}
				for _, check := range report.Checks {
					status := "ok"
					if check.Skipped {
						status = "skipped"
					} else if !check.OK {
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
	if macosOnly {
		cmd.Use = "verify-macos <name>"
		cmd.Short = "Verify a macOS guest (compatibility command)"
		cmd.Hidden = true
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 2*time.Minute, "total diagnostic timeout (no boot retries)")
	return cmd
}
