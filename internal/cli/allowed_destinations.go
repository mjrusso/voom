package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func allowedDestinationsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "allowed-destinations",
		Short: "Show global exceptions to the tailnet block",
		Long:  "Show IP:PORT exceptions to the guest tailnet block. The list applies to all VMs.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := openStore()
			if err != nil {
				return err
			}
			config, err := store.LoadNetworkConfig()
			if err != nil {
				return err
			}
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(config)
			}
			for _, dest := range config.AllowedDestinations {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), dest)
			}
			return nil
		},
	}
	setDestinations := func(cmd *cobra.Command, args []string) error {
		store, err := openStore()
		if err != nil {
			return err
		}
		config, err := store.SetAllowedDestinations(args)
		if err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(config)
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "updated allowed destinations; changes take effect at VM startup")
		return nil
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "set <IP:PORT...>",
		Short: "Replace global exceptions to the tailnet block",
		Long:  "Replace the IP:PORT exceptions to the guest tailnet block for all VMs. Each exception allows TCP and UDP. Use literal IP addresses and ports 1-65535; bracket IPv6 addresses. Changes take effect when each VM next starts.",
		Args:  cobra.MinimumNArgs(1),
		RunE:  setDestinations,
	}, &cobra.Command{
		Use:   "clear",
		Short: "Clear global exceptions to the tailnet block",
		Long:  "Remove all exceptions to the guest tailnet block. Changes take effect when each VM next starts.",
		Args:  cobra.NoArgs,
		RunE:  setDestinations,
	})
	return cmd
}
