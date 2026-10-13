package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mjrusso/voom/internal/pci"
)

func pciCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "pci", Aliases: []string{"gpu"}, Short: "Manage Linux PCI passthrough, such as GPUs"}
	cmd.AddCommand(&cobra.Command{
		Use:   "discover",
		Short: "List host PCI Express slots available for passthrough",
		Long:  "List the PCI slots on this Linux host that contain something other than bridges. Each row contains the slot address, each function's vendor:device ID and driver, and whether the slot can be assigned or why not.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			slots, err := pci.Discover()
			if err != nil {
				return err
			}
			type row struct {
				Slot    pci.Slot     `json:"slot"`
				Devices []pci.Device `json:"devices"`
				Ready   bool         `json:"ready"`
				Error   string       `json:"error,omitempty"`
			}
			rows := make([]row, len(slots))
			for i, slot := range slots {
				rows[i] = row{Slot: slot.Slot, Devices: slot.Devices, Ready: slot.Err == nil}
				if slot.Err != nil {
					rows[i].Error = slot.Err.Error()
				}
			}
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
			}
			tw := tableWriter(cmd)
			for _, r := range rows {
				functions := make([]string, len(r.Devices))
				for i, device := range r.Devices {
					driver := device.Driver
					if driver == "" {
						driver = "none"
					}
					functions[i] = fmt.Sprintf(".%d %s %s", device.Number, device.ID(), driver)
				}
				status := "ready"
				if !r.Ready {
					status = "unavailable: " + r.Error
				}
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Slot, strings.Join(functions, ", "), status)
			}
			return tw.Flush()
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "add <name> <device-name> <slot-or-function>",
		Short: "Assign a host PCI Express slot or function to a VM",
		Long:  "Assign a host PCI Express slot, such as 0000:41:00, or one of its functions, such as 0000:41:00.0, to a stopped QEMU VM. A slot assigns all of its functions. The assigned functions must be bound to vfio-pci, and every other device in their IOMMU groups must be bound to vfio-pci or have no driver. Voom records each function's vendor:device ID and refuses to start the VM if the hardware at that address changes.",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := loadRuntimeDeps()
			if err != nil {
				return err
			}
			decl, err := deps.vm.AddPCIDevice(cmd.Context(), args[0], args[1], args[2])
			if err != nil {
				return err
			}
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": args[0], "changed": true, "pciDevice": decl})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "assigned PCI device %s at %s to %s\n", decl.Name, decl.Selector(), args[0])
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:     "remove <name> <device-name>",
		Aliases: []string{"rm", "delete"},
		Short:   "Remove a PCI assignment from a stopped VM",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := loadRuntimeDeps()
			if err != nil {
				return err
			}
			if err := deps.vm.RemovePCIDevice(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": args[0], "pciDevice": args[1], "changed": true})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "removed PCI device %s from %s\n", args[1], args[0])
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:     "list <name>",
		Aliases: []string{"ls"},
		Short:   "List a VM's PCI assignments",
		Args:    cobra.ExactArgs(1),
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
				return json.NewEncoder(cmd.OutOrStdout()).Encode(vmRec.PCIDevices)
			}
			tw := tableWriter(cmd)
			for _, decl := range vmRec.PCIDevices {
				ids := make([]string, len(decl.Functions))
				for i, f := range decl.Functions {
					ids[i] = f.ID()
				}
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", decl.Name, decl.Selector(), strings.Join(ids, " "))
			}
			return tw.Flush()
		},
	})
	return cmd
}
