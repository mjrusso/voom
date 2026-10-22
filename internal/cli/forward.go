package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/state"
	"github.com/mjrusso/voom/internal/vm"
)

func forwardCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "forward", Short: "Manage guest port forwards"}
	cmd.AddCommand(forwardAddCommand(), forwardRemoveCommand(), forwardListCommand(), forwardDiscoverCommand(), forwardAutoCommand())
	return cmd
}

func forwardAddCommand() *cobra.Command {
	var hostPort int
	var bind, protocol string
	var lan, auto bool
	cmd := &cobra.Command{Use: "add <name> <guest-port>", ValidArgsFunction: completeVMNames, Short: "Add a manual TCP or UDP forward", Long: "Add a manual TCP or UDP forward to a Linux or MacOS guest. No guest services are required. The application must listen on the guest's network address or 0.0.0.0.", Example: "  voom forward add dev 8080 --protocol tcp --host-port 18080 --bind 127.0.0.1\n  voom forward add dev 5353 --protocol udp --host-port 15353 --bind 127.0.0.1", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		protocol, err := forward.NormalizeProtocol(protocol)
		if err != nil {
			return err
		}
		guestPort, err := state.ParsePort(args[1])
		if err != nil {
			return err
		}
		if hostPort != 0 {
			if _, err := state.ParsePort(strconv.Itoa(hostPort)); err != nil {
				return err
			}
		}
		if lan && bind != "" {
			return errors.New("--lan and --bind are mutually exclusive")
		}
		if auto && hostPort != 0 {
			return errors.New("--auto and --host-port are mutually exclusive")
		}
		if lan {
			bind = "0.0.0.0"
		}
		if bind == "" {
			bind = "127.0.0.1"
		}
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		fwd, err := deps.vm.AddForward(cmd.Context(), args[0], guestPort, hostPort, bind, auto, protocol)
		if err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": args[0], "changed": true, "forward": fwd})
		}
		guestTargetIP := state.DefaultGuestTargetIP("qemu")
		if vmRec, loadErr := deps.store.LoadVM(args[0]); loadErr == nil {
			guestTargetIP = state.DefaultGuestTargetIP(vmRec.Driver)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s -> %s\n", args[0], fwd.Protocol, forward.Address(fwd.Bind, fwd.HostPort), forward.Address(guestTargetIP, fwd.GuestPort))
		return nil
	}}
	cmd.Flags().IntVar(&hostPort, "host-port", 0, "host port")
	cmd.Flags().StringVar(&protocol, "protocol", "tcp", "transport protocol (tcp or udp)")
	cmd.Flags().StringVar(&bind, "bind", "", "host bind address")
	cmd.Flags().BoolVar(&lan, "lan", false, "bind to 0.0.0.0")
	cmd.Flags().BoolVar(&auto, "auto", false, "allocate a free host port")
	return cmd
}

func forwardRemoveCommand() *cobra.Command {
	var bind, protocol string
	cmd := &cobra.Command{Use: "remove <name> <host-port>", ValidArgsFunction: completeVMNames, Aliases: []string{"rm", "delete"}, Short: "Remove a manual TCP or UDP forward", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		protocol, err := forward.NormalizeProtocol(protocol)
		if err != nil {
			return err
		}
		port, err := state.ParsePort(args[1])
		if err != nil {
			return err
		}
		if bind == "" {
			bind = "127.0.0.1"
		}
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		if err := deps.vm.RemoveForward(cmd.Context(), args[0], port, bind, protocol); err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": args[0], "hostPort": port, "bind": bind, "protocol": protocol, "changed": true})
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "removed %s forward %s from %s\n", protocol, forward.Address(bind, port), args[0])
		return nil
	}}
	cmd.Flags().StringVar(&protocol, "protocol", "tcp", "transport protocol (tcp or udp)")
	cmd.Flags().StringVar(&bind, "bind", "127.0.0.1", "host bind address")
	return cmd
}

func forwardListCommand() *cobra.Command {
	return &cobra.Command{Use: "list [name]", ValidArgsFunction: completeVMNames, Aliases: []string{"ls"}, Short: "List effective forwards", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		rows, err := deps.vm.ForwardRows(cmd.Context(), args)
		if err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
		}
		for _, r := range rows {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\t%s\tinstalled=%t", r.VM, r.Kind, r.Protocol, forward.Address(r.Bind, r.HostPort), forward.Address(r.GuestTargetIP, r.GuestPort), r.Status, r.Installed)
			if r.Kind == "auto" {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\toffset=%d", r.Offset)
			}
			if r.Reason != "" {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\t%s", r.Reason)
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout())
		}
		return nil
	}}
}

func forwardDiscoverCommand() *cobra.Command {
	return &cobra.Command{Use: "discover <name>", ValidArgsFunction: completeVMNames, Short: "Preview forwarding state", Long: "Preview application forwarding from a Linux guest listener report. Guest listener reporting is not supported for MacOS guests.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
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
		if err := vm.RequireGuestPortReport(im, "forwarding discovery"); err != nil {
			return err
		}
		report, err := deps.vm.ReadGuestPorts(vmRec)
		if err != nil {
			return err
		}
		runtimeForwards, err := deps.vm.ReadRuntimeAutoForwards(vmRec)
		if err != nil {
			return err
		}
		var preview []forward.Row
		if vmRec.Network.AutoForward {
			plans, err := deps.vm.PlanAutoForwards(vmRec, *report, runtimeForwards)
			if err != nil {
				return err
			}
			preview = make([]forward.Row, 0, len(plans))
			for _, plan := range plans {
				preview = append(preview, plan.Preview())
			}
		}
		autoBind := vmRec.Network.AutoForwardBind
		if autoBind == "" {
			autoBind = "127.0.0.1"
		}
		out := map[string]any{"listeners": report.Listeners, "manualForwards": vmRec.Network.Forwards, "runtimeAutoForwards": runtimeForwards, "autoForwardPreview": preview, "autoForward": vmRec.Network.AutoForward, "autoForwardOffset": vmRec.Network.AutoForwardHostOffset, "autoForwardBind": autoBind}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
		}
		for _, l := range report.Listeners {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s:%d\tforwardable=%t\n", l.Proto, l.Addr, l.Port, forward.ListenerForwardable(l))
		}
		for _, f := range preview {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "auto\t%s\t%s\t%s\t%s\tinstalled=%t\toffset=%d", f.Protocol, f.Status, forward.Address(f.Bind, f.HostPort), forward.Address(f.GuestTargetIP, f.GuestPort), f.Installed, f.Offset)
			if f.Reason != "" {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\t%s", f.Reason)
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout())
		}
		return nil
	}}
}

func forwardAutoCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "auto", Short: "Configure auto-forwarding"}
	enableOffset := 0
	enableBind := ""
	enableLan := false
	enable := &cobra.Command{Use: "enable <name>", ValidArgsFunction: completeVMNames, Short: "Enable auto-forwarding", Long: "Enable automatic TCP forwarding from a Linux guest's listener reports.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if enableLan && enableBind != "" {
			return errors.New("--lan and --bind are mutually exclusive")
		}
		bind := enableBind
		if enableLan {
			bind = "0.0.0.0"
		}
		if bind == "" {
			bind = "127.0.0.1"
		}
		enabled := true
		return updateAutoForward(cmd, args[0], vm.AutoForwardUpdate{Enabled: &enabled, Offset: &enableOffset, Bind: &bind})
	}}
	enable.Flags().IntVar(&enableOffset, "offset", 0, "host port offset")
	enable.Flags().StringVar(&enableBind, "bind", "", "host bind address")
	enable.Flags().BoolVar(&enableLan, "lan", false, "bind to 0.0.0.0")
	cmd.AddCommand(enable)
	cmd.AddCommand(&cobra.Command{Use: "disable <name>", ValidArgsFunction: completeVMNames, Short: "Disable auto-forwarding", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		enabled := false
		return updateAutoForward(cmd, args[0], vm.AutoForwardUpdate{Enabled: &enabled})
	}})
	cmd.AddCommand(&cobra.Command{Use: "offset <name> <n>", ValidArgsFunction: completeVMNames, Short: "Set auto-forward offset", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 0 {
			return errors.New("offset must be a non-negative integer")
		}
		return updateAutoForward(cmd, args[0], vm.AutoForwardUpdate{Offset: &n})
	}})
	reconcile := &cobra.Command{Use: "reconcile <name>", ValidArgsFunction: completeVMNames, Short: "Reconcile runtime auto-forwards", Args: cobra.ExactArgs(1), Hidden: true, RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		rows, err := deps.vm.ReconcileAutoForwards(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
		}
		tw := tableWriter(cmd)
		for _, r := range rows {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Protocol, r.Status, forward.Address(r.Bind, r.HostPort), forward.Address(r.GuestTargetIP, r.GuestPort), r.Reason)
		}
		return tw.Flush()
	}}
	cmd.AddCommand(reconcile)
	watch := &cobra.Command{Use: "watch <id>", Short: "Watch guest port reports and reconcile runtime auto-forwards", Args: cobra.ExactArgs(1), Hidden: true, RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		return deps.vm.WatchAutoForwards(cmd.Context(), args[0])
	}}
	cmd.AddCommand(watch)
	return cmd
}
