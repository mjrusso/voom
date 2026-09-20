package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mjrusso/voom/internal/usb"
)

func usbCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "usb", Short: "Manage Linux USB passthrough"}
	cmd.AddCommand(&cobra.Command{
		Use:   "discover",
		Short: "List host USB devices available for passthrough",
		Long:  "List non-hub USB devices connected to this Linux host. Each row contains the topology location, vendor and product IDs, serial number, product name, and current read/write accessibility.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inventory, err := usb.Scan()
			if err != nil {
				return err
			}
			devices := inventory.Devices()
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(devices)
			}
			tw := tableWriter(cmd)
			for _, device := range devices {
				access := "yes"
				if !device.Accessible {
					access = "no: " + device.AccessError
				}
				_, _ = fmt.Fprintf(tw, "%s\t%s:%s\t%s\t%s\t%s\n", device.Location(), device.VendorID, device.ProductID, device.Serial, device.Product, access)
			}
			return tw.Flush()
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "add <vm> <name> <location>",
		Short: "Assign a host USB topology route to a VM",
		Long:  "Assign a stable USB topology location reported by 'voom usb discover', such as usb-0000:00:14.0@2-3.2, to a QEMU VM. A running VM receives the device immediately. Any device occupying that route while the VM runs is exposed to the guest.",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := loadRuntimeDeps()
			if err != nil {
				return err
			}
			decl, err := deps.vm.AddUSBDevice(cmd.Context(), args[0], args[1], args[2])
			if err != nil {
				return err
			}
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": args[0], "changed": true, "usbDevice": decl})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "assigned USB device %s at %s to %s\n", decl.Name, decl.Location(), args[0])
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "rm <vm> <name>",
		Short: "Remove a USB assignment from a VM",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := loadRuntimeDeps()
			if err != nil {
				return err
			}
			if err := deps.vm.RemoveUSBDevice(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": args[0], "usbDevice": args[1], "changed": true})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "removed USB device %s from %s\n", args[1], args[0])
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "ls <vm>",
		Short: "List a VM's USB assignments",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := loadRuntimeDeps()
			if err != nil {
				return err
			}
			vmRec, err := deps.store.LoadVM(args[0])
			if err != nil {
				return err
			}
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(vmRec.USBDevices)
			}
			tw := tableWriter(cmd)
			for _, device := range vmRec.USBDevices {
				_, _ = fmt.Fprintf(tw, "%s\t%s\n", device.Name, device.Location())
			}
			return tw.Flush()
		},
	})
	return cmd
}
