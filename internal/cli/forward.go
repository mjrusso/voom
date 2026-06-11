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
	cmd.AddCommand(forwardAddCommand(), forwardRmCommand(), forwardLsCommand(), forwardDiscoverCommand(), forwardAutoCommand())
	return cmd
}

func forwardAddCommand() *cobra.Command {
	var hostPort int
	var bind string
	var lan, auto bool
	cmd := &cobra.Command{Use: "add <name> <guest-port>", Short: "Add a manual forward", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
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
		unlock, err := deps.store.LockGlobalAndReload()
		if err != nil {
			return err
		}
		defer unlock()
		fwd, err := deps.vm.AddForward(args[0], guestPort, hostPort, bind, auto)
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
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s %s:%d -> %s:%d\n", args[0], fwd.Bind, fwd.HostPort, guestTargetIP, fwd.GuestPort)
		return nil
	}}
	cmd.Flags().IntVar(&hostPort, "host-port", 0, "host port")
	cmd.Flags().StringVar(&bind, "bind", "", "host bind address")
	cmd.Flags().BoolVar(&lan, "lan", false, "bind to 0.0.0.0")
	cmd.Flags().BoolVar(&auto, "auto", false, "allocate a free host port")
	return cmd
}

func forwardRmCommand() *cobra.Command {
	var bind string
	cmd := &cobra.Command{Use: "rm <name> <host-port>", Short: "Remove a manual forward", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
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
		if err := deps.vm.RemoveForward(args[0], port, bind); err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": args[0], "hostPort": port, "bind": bind, "changed": true})
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "removed forward %s:%d from %s\n", bind, port, args[0])
		return nil
	}}
	cmd.Flags().StringVar(&bind, "bind", "127.0.0.1", "host bind address")
	return cmd
}

func forwardLsCommand() *cobra.Command {
	return &cobra.Command{Use: "ls [name]", Short: "List effective forwards", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		rows, err := deps.vm.ForwardRows(args)
		if err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
		}
		for _, r := range rows {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s:%d\t%s:%d\t%s\tinstalled=%t", r.VM, r.Kind, r.Protocol, r.Bind, r.HostPort, r.GuestTargetIP, r.GuestPort, r.Status, r.Installed)
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
	return &cobra.Command{Use: "discover <name>", Short: "Preview forwarding state", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
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
		preview := deps.vm.PlanAutoForwards(vmRec, report, runtimeForwards)
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
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "auto\t%s\t%s:%d\t%s:%d\tinstalled=%t\toffset=%d", f.Status, f.Bind, f.HostPort, f.GuestTargetIP, f.GuestPort, f.Installed, f.Offset)
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
	enable := &cobra.Command{Use: "enable <name>", Short: "Enable auto-forwarding", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
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
		return setAutoForward(cmd, args[0], true, enableOffset, true, bind, true)
	}}
	enable.Flags().IntVar(&enableOffset, "offset", 0, "host port offset")
	enable.Flags().StringVar(&enableBind, "bind", "", "host bind address")
	enable.Flags().BoolVar(&enableLan, "lan", false, "bind to 0.0.0.0")
	cmd.AddCommand(enable)
	cmd.AddCommand(&cobra.Command{Use: "disable <name>", Short: "Disable auto-forwarding", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return setAutoForward(cmd, args[0], false, 0, false, "", false)
	}})
	cmd.AddCommand(&cobra.Command{Use: "offset <name> <n>", Short: "Set auto-forward offset", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 0 {
			return errors.New("offset must be a non-negative integer")
		}
		return setAutoForwardOffset(cmd, args[0], n)
	}})
	reconcile := &cobra.Command{Use: "reconcile <name>", Short: "Reconcile runtime auto-forwards", Args: cobra.ExactArgs(1), Hidden: true, RunE: func(cmd *cobra.Command, args []string) error {
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
		if err := vm.RequireGuestPortReport(im, "auto-forward reconciliation"); err != nil {
			return err
		}
		rows, err := deps.vm.ReconcileAutoForwards(cmd.Context(), vmRec)
		if err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
		}
		tw := tableWriter(cmd)
		for _, r := range rows {
			_, _ = fmt.Fprintf(tw, "%s\t%s:%d\t%s:%d\t%s\n", r.Status, r.Bind, r.HostPort, r.GuestTargetIP, r.GuestPort, r.Reason)
		}
		return tw.Flush()
	}}
	cmd.AddCommand(reconcile)
	watch := &cobra.Command{Use: "watch <name>", Short: "Watch guest port reports and reconcile runtime auto-forwards", Args: cobra.ExactArgs(1), Hidden: true, RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		return deps.vm.WatchAutoForwards(cmd.Context(), args[0])
	}}
	cmd.AddCommand(watch)
	return cmd
}
