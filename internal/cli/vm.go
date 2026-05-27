package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mjrusso/voom/internal/state"
	"github.com/mjrusso/voom/internal/vm"
)

func createCommand() *cobra.Command {
	var imageName, driver, memory string
	var cpus, sshPort int
	var start bool
	cmd := &cobra.Command{
		Use:   "create <name> --image <image>",
		Short: "Create a stopped VM from an image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := state.ValidateName("VM", args[0]); err != nil {
				return err
			}
			if imageName == "" {
				return errors.New("--image is required")
			}
			mem, err := state.ParseMemoryMiB(memory)
			if err != nil {
				return err
			}
			if cpus < 1 {
				return errors.New("--cpus must be at least 1")
			}
			deps, err := loadRuntimeDeps()
			if err != nil {
				return err
			}
			unlock, err := deps.store.LockGlobal()
			if err != nil {
				return err
			}
			vmRec, err := deps.vm.Create(cmd.Context(), args[0], imageName, driver, cpus, mem, sshPort)
			unlock()
			if err != nil {
				return err
			}
			started := false
			if start {
				startedVM, err := deps.vm.Start(cmd.Context(), cmd.ErrOrStderr(), vmRec.Name)
				if err != nil {
					return err
				}
				vmRec = startedVM
				started = true
			}
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": vmRec.Name, "id": vmRec.ID, "changed": true, "started": started, "sshPort": vmRec.Network.SSHPort})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "created VM %s (%s), ssh 127.0.0.1:%d\n", vmRec.Name, vmRec.ID, vmRec.Network.SSHPort)
			return nil
		},
	}
	cmd.Flags().StringVar(&imageName, "image", "", "image name")
	cmd.Flags().StringVar(&driver, "driver", "auto", "driver: auto, qemu, or vfkit")
	cmd.Flags().IntVar(&cpus, "cpus", 4, "CPU count")
	cmd.Flags().StringVar(&memory, "memory", "4096MiB", "memory size")
	cmd.Flags().IntVar(&sshPort, "ssh-port", 0, "explicit host SSH port (0 = auto-allocate)")
	cmd.Flags().BoolVar(&start, "start", false, "start after creation")
	return cmd
}

func startCommand() *cobra.Command {
	return &cobra.Command{Use: "start <name>", Short: "Start a VM", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		vmRec, err := deps.vm.Start(cmd.Context(), cmd.ErrOrStderr(), args[0])
		if err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": args[0], "changed": true, "sshPort": vmRec.Network.SSHPort})
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "started VM %s, ssh 127.0.0.1:%d\n", args[0], vmRec.Network.SSHPort)
		return nil
	}}
}

func stopCommand() *cobra.Command {
	return &cobra.Command{Use: "stop <name>", Short: "Stop a VM", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		changed, err := deps.vm.Stop(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": args[0], "changed": changed})
		}
		if changed {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "stopped VM %s\n", args[0])
		} else {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "VM %s is not running\n", args[0])
		}
		return nil
	}}
}

func restartCommand() *cobra.Command {
	return &cobra.Command{Use: "restart <name>", Short: "Restart a VM", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		// Best-effort stop: a not-running VM is the common case; surface failures via Start.
		_, _ = deps.vm.Stop(cmd.Context(), args[0])
		vmRec, err := deps.vm.Start(cmd.Context(), cmd.ErrOrStderr(), args[0])
		if err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": args[0], "changed": true, "sshPort": vmRec.Network.SSHPort})
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "restarted VM %s\n", args[0])
		return nil
	}}
}

func sshCommand() *cobra.Command {
	return &cobra.Command{Use: "ssh <name> [--] [command...]", Short: "SSH into a VM, or run a one-shot command", Long: "Open an interactive SSH session to a running VM. Any trailing arguments are passed through to ssh as a remote command, so `voom ssh <name> -- uname -a` runs `uname -a` in the VM and exits. The `--` separator is optional but recommended when the remote command itself takes flags.", Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		vmRec, err := deps.store.LoadVM(args[0])
		if err != nil {
			return err
		}
		if !deps.vm.IsRunning(vmRec) {
			return fmt.Errorf("VM %q is not running", vmRec.Name)
		}
		sshArgs := vm.SSHBaseArgs(vmRec)
		sshArgs = append(sshArgs, fmt.Sprintf("%s@%s", vmRec.Access.SSHUser, vmRec.Network.SSHBind))
		remoteArgs := args[1:]
		if len(remoteArgs) > 0 && remoteArgs[0] == "--" {
			remoteArgs = remoteArgs[1:]
		}
		sshArgs = append(sshArgs, remoteArgs...)
		c := exec.CommandContext(cmd.Context(), "ssh", sshArgs...)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		return c.Run()
	}}
}

func sshConfigCommand() *cobra.Command {
	return &cobra.Command{Use: "ssh-config <name>", Short: "Print OpenSSH config", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		vmRec, err := deps.store.LoadVM(args[0])
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Host voom-%s\n  HostName %s\n  Port %d\n  User %s\n  StrictHostKeyChecking no\n  UserKnownHostsFile /dev/null\n", vmRec.Name, vmRec.Network.SSHBind, vmRec.Network.SSHPort, vmRec.Access.SSHUser)
		if vmRec.Access.SSHIdentityPath != "" {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  IdentityFile %s\n", vmRec.Access.SSHIdentityPath)
		}
		return nil
	}}
}

func consoleCommand() *cobra.Command {
	return &cobra.Command{Use: "console <name>", Short: "Follow serial console log", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		vmRec, err := deps.store.LoadVM(args[0])
		if err != nil {
			return err
		}
		c := exec.CommandContext(cmd.Context(), "tail", "-n", "+1", "-F", deps.store.LogPath(vmRec, "serial"))
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		return c.Run()
	}}
}

func logsCommand() *cobra.Command {
	var kind string
	cmd := &cobra.Command{Use: "logs <name>", Short: "Print VM logs", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		vmRec, err := deps.store.LoadVM(args[0])
		if err != nil {
			return err
		}
		b, err := os.ReadFile(deps.store.LogPath(vmRec, kind))
		if os.IsNotExist(err) {
			return fmt.Errorf("log %q is not available for VM %q", kind, vmRec.Name)
		}
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(b)
		return err
	}}
	cmd.Flags().StringVar(&kind, "kind", "serial", "log kind: serial, qemu, vfkit, gvproxy, share-mount")
	return cmd
}

func infoCommand() *cobra.Command {
	return &cobra.Command{Use: "info <name>", Short: "Inspect a VM", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		vmRec, err := deps.store.LoadVM(args[0])
		if err != nil {
			return err
		}
		out := vmInfo{VM: vmRec, Running: deps.vm.IsRunning(vmRec), DiskPath: deps.store.VMDiskPath(vmRec)}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "name: %s\nid: %s\nstatus: %s\nimage: %s\nssh: %s@%s:%d\nforwards: %d\nshares: %d\n", vmRec.Name, vmRec.ID, vm.Status(out.Running), vmRec.Image.Name, vmRec.Access.SSHUser, vmRec.Network.SSHBind, vmRec.Network.SSHPort, len(vmRec.Network.Forwards), len(vmRec.Shares))
		return nil
	}}
}

type vmInfo struct {
	VM       *state.VMRecord `json:"vm"`
	Running  bool            `json:"running"`
	DiskPath string          `json:"diskPath"`
}

func listCommand() *cobra.Command {
	return &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List VMs", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		vms, err := deps.store.ListVMs()
		if err != nil {
			return err
		}
		type row struct {
			Name    string `json:"name"`
			ID      string `json:"id"`
			Status  string `json:"status"`
			Image   string `json:"image"`
			SSHPort int    `json:"sshPort"`
		}
		rows := []row{}
		for _, vmRec := range vms {
			rows = append(rows, row{vmRec.Name, vmRec.ID, vm.Status(deps.vm.IsRunning(vmRec)), vmRec.Image.Name, vmRec.Network.SSHPort})
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
		}
		for _, r := range rows {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%d\n", r.Name, r.ID, r.Status, r.Image, r.SSHPort)
		}
		return nil
	}}
}

func renameCommand() *cobra.Command {
	return &cobra.Command{Use: "rename <old-name> <new-name>", Short: "Rename a VM", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if err := state.ValidateName("VM", args[1]); err != nil {
			return err
		}
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		unlock, err := deps.store.LockGlobal()
		if err != nil {
			return err
		}
		defer unlock()
		vmRec, err := deps.store.RenameVM(args[0], args[1])
		if err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"oldName": args[0], "name": args[1], "id": vmRec.ID, "changed": true})
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "renamed VM %s to %s\n", args[0], args[1])
		return nil
	}}
}

func rmCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{Use: "rm <name>", Aliases: []string{"remove", "destroy"}, Short: "Remove a VM", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !force {
			if outputFormat(cmd) == "json" || !isTerminalFunc(os.Stdin) {
				return errors.New("refusing to remove without --force in non-interactive or JSON mode")
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Remove VM %s? Type the name to confirm: ", args[0])
			line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
			if strings.TrimSpace(line) != args[0] {
				return errors.New("remove cancelled")
			}
		}
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		unlock, err := deps.store.LockGlobal()
		if err != nil {
			return err
		}
		defer unlock()
		if err := deps.vm.Remove(cmd.Context(), args[0]); err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": args[0], "changed": true})
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "removed VM %s\n", args[0])
		return nil
	}}
	cmd.Flags().BoolVar(&force, "force", false, "remove without prompting")
	return cmd
}

func diskCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "disk", Short: "Manage VM disks"}
	var resetImage string
	cmd.AddCommand(&cobra.Command{Use: "grow <name> [amount]", Short: "Grow a stopped VM disk", Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		amount := "10G"
		if len(args) == 2 {
			amount = args[1]
		}
		if err := deps.vm.GrowDisk(cmd.Context(), args[0], amount); err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": args[0], "changed": true, "amount": amount})
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "grew VM %s disk by %s\n", args[0], amount)
		return nil
	}})
	reset := &cobra.Command{Use: "reset <name> --image <image>", Short: "Reset VM disk from image", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if resetImage == "" {
			return errors.New("--image is required")
		}
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		if err := deps.vm.ResetDisk(cmd.Context(), args[0], resetImage); err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": args[0], "image": resetImage, "changed": true})
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "reset VM %s disk from image %s\n", args[0], resetImage)
		return nil
	}}
	reset.Flags().StringVar(&resetImage, "image", "", "image name")
	cmd.AddCommand(reset)
	return cmd
}
