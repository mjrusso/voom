package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mjrusso/voom/internal/state"
	"github.com/mjrusso/voom/internal/vm"
)

func createCommand() *cobra.Command {
	var imageName, driver, memory string
	var cpus, sshPort int
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
			vmRec, err := deps.vm.Create(cmd.Context(), args[0], imageName, driver, cpus, mem, sshPort)
			if err != nil {
				return err
			}
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": vmRec.Name, "id": vmRec.ID, "changed": true, "sshPort": vmRec.Network.SSHPort, "cpus": vmRec.Resources.CPUs, "memoryMiB": vmRec.Resources.MemoryMiB})
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
	return cmd
}

func cloneCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "clone <source-name> <new-name>",
		Short: "Clone a stopped VM into a new VM",
		Long:  "Create a new VM by copying a stopped VM's current disk. The clone gets a fresh ID and a newly-allocated SSH port and keeps the source's resources and access. It does not inherit the source's shares, USB assignments, manual forwards, or auto-forward settings, but prints the commands to reproduce them on the clone.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := state.ValidateName("VM", args[1]); err != nil {
				return err
			}
			deps, err := loadRuntimeDeps()
			if err != nil {
				return err
			}
			vmRec, err := deps.vm.Clone(cmd.Context(), args[0], args[1])
			if err != nil {
				return err
			}
			reproduce := []string{}
			omitted := []omittedConfig{}
			if src, loadErr := deps.store.LoadVM(args[0]); loadErr == nil {
				reproduce = replayCommands(src, vmRec.Name)
				if src.Network.Egress != nil {
					omitted = append(omitted, omittedConfig{Config: "egress", Reason: "backend socket belongs exclusively to the source VM ID", CloneID: vmRec.ID})
				}
			}
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": vmRec.Name, "id": vmRec.ID, "source": args[0], "changed": true, "sshPort": vmRec.Network.SSHPort, "cpus": vmRec.Resources.CPUs, "memoryMiB": vmRec.Resources.MemoryMiB, "configCommands": reproduce, "omittedConfig": omitted})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "cloned VM %s to %s (%s), ssh 127.0.0.1:%d\n", args[0], vmRec.Name, vmRec.ID, vmRec.Network.SSHPort)
			if len(omitted) > 0 {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "egress attachment omitted; provision a unique backend for clone VM ID %s\n", vmRec.ID)
			}
			if len(reproduce) > 0 {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "to reproduce %s's configuration on %s, run:\n", args[0], vmRec.Name)
				for _, c := range reproduce {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", c)
				}
			}
			return nil
		},
	}
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
		if usage, err := state.ReadDiskUsage(out.DiskPath); err == nil {
			out.Disk = &usage
		} else {
			out.DiskError = err.Error()
		}
		if out.Running && vmRec.Network.Egress != nil {
			observed, _ := deps.vm.ObserveEgress(cmd.Context(), vmRec)
			out.EgressRuntime = &observed
		}
		out.USBStatus = deps.vm.ObserveUSB(cmd.Context(), vmRec)
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
		}
		disk := "unavailable: " + out.DiskError
		if out.Disk != nil {
			disk = fmt.Sprintf("%s virtual, %s allocated on host", formatBytes(out.Disk.VirtualBytes), formatBytes(out.Disk.AllocatedBytes))
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "name: %s\nid: %s\nstatus: %s\nimage: %s\ncpus: %d\nmemory: %dMiB\ndisk: %s\nssh: %s@%s:%d\nforwards: %d\nshares: %d\nusb: %d\n", vmRec.Name, vmRec.ID, vm.Status(out.Running), vmRec.Image.Name, vmRec.Resources.CPUs, vmRec.Resources.MemoryMiB, disk, vmRec.Access.SSHUser, vmRec.Network.SSHBind, vmRec.Network.SSHPort, len(vmRec.Network.Forwards), len(vmRec.Shares), len(vmRec.USBDevices))
		for _, device := range out.USBStatus {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "usb-device: %s %s host=%s runtime=%s\n", device.Name, device.Location, device.Host.State, device.Runtime.State)
			if device.Host.Error != "" {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "usb-host-error: %s: %s\n", device.Name, device.Host.Error)
			}
			if device.Runtime.Error != "" {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "usb-runtime-error: %s: %s\n", device.Name, device.Runtime.Error)
			}
		}
		status := egressStatus(vmRec.Network.Egress, out.EgressRuntime)
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "egress: %s; direct egress remains available\n", status)
		return nil
	}}
}

type vmInfo struct {
	EgressRuntime *vm.EgressRuntime `json:"egressRuntime,omitempty"`
	VM            *state.VMRecord   `json:"vm"`
	Running       bool              `json:"running"`
	DiskPath      string            `json:"diskPath"`
	Disk          *state.DiskUsage  `json:"disk,omitempty"`
	DiskError     string            `json:"diskError,omitempty"`
	USBStatus     []vm.USBStatus    `json:"usbStatus,omitempty"`
}

type omittedConfig struct {
	Config  string `json:"config"`
	Reason  string `json:"reason"`
	CloneID string `json:"cloneID"`
}

func listCommand() *cobra.Command {
	return &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List VMs", Long: "List VMs, one per row: name, ID, status, image, CPUs, memory, SSH port, disk capacity, configured USB count, and saved egress configuration. A disk that cannot be read shows as disk=?. The USB count and egress-config columns show saved configuration; 'voom info' reports observed USB and egress state and host disk allocation.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		vms, err := deps.store.ListVMs()
		if err != nil {
			return err
		}
		type row struct {
			Name         string           `json:"name"`
			ID           string           `json:"id"`
			Status       string           `json:"status"`
			Image        string           `json:"image"`
			CPUs         int              `json:"cpus"`
			MemoryMiB    int              `json:"memoryMiB"`
			SSHPort      int              `json:"sshPort"`
			Disk         *state.DiskUsage `json:"disk,omitempty"`
			USB          int              `json:"usb"`
			EgressConfig string           `json:"egressConfig"`
		}
		rows := []row{}
		for _, vmRec := range vms {
			var disk *state.DiskUsage
			if usage, err := state.ReadDiskUsage(deps.store.VMDiskPath(vmRec)); err == nil {
				disk = &usage
			}
			rows = append(rows, row{
				Name:         vmRec.Name,
				ID:           vmRec.ID,
				Status:       vm.Status(deps.vm.IsRunning(vmRec)),
				Image:        vmRec.Image.Name,
				CPUs:         vmRec.Resources.CPUs,
				MemoryMiB:    vmRec.Resources.MemoryMiB,
				SSHPort:      vmRec.Network.SSHPort,
				Disk:         disk,
				USB:          len(vmRec.USBDevices),
				EgressConfig: egressConfig(vmRec.Network.Egress),
			})
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
		}
		tw := tableWriter(cmd)
		for _, r := range rows {
			disk := "?"
			if r.Disk != nil {
				disk = formatBytes(r.Disk.VirtualBytes)
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%dMiB\t%d\tdisk=%s\tusb=%d\tegress-config=%s\n", r.Name, r.ID, r.Status, r.Image, r.CPUs, r.MemoryMiB, r.SSHPort, disk, r.USB, r.EgressConfig)
		}
		return tw.Flush()
	}}
}

func formatBytes(n int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	v, i := float64(n), 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return strconv.FormatInt(n, 10) + "B"
	}
	return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0") + units[i]
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
		vmRec, err := deps.store.RenameVM(cmd.Context(), args[0], args[1])
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

func removeCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{Use: "remove <name>", Aliases: []string{"rm", "destroy"}, Short: "Remove a VM", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		vmRec, err := deps.store.LoadVM(args[0])
		if err != nil {
			return err
		}
		if vmRec.Network.Egress != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "VM %s has an external egress attachment.\nRemoving the VM will not revoke or remove its backend.\nRun the provider's detach command first.\n", vmRec.Name)
		}
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

func resourcesCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "resources", Short: "Manage VM resource allocations"}
	disk := &cobra.Command{Use: "disk", Short: "Manage VM disk allocation"}
	disk.AddCommand(diskGrowCommand())
	cmd.AddCommand(disk)
	cmd.AddCommand(&cobra.Command{Use: "cpus <name> <n>", Short: "Set CPU allocation for a stopped VM", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		cpus, err := strconv.Atoi(args[1])
		if err != nil || cpus < 1 {
			return errors.New("cpus must be at least 1")
		}
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		vmRec, changed, err := deps.vm.SetCPUs(cmd.Context(), args[0], cpus)
		if err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": vmRec.Name, "changed": changed, "cpus": vmRec.Resources.CPUs})
		}
		if changed {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "set VM %s CPUs to %d\n", vmRec.Name, vmRec.Resources.CPUs)
		} else {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "VM %s CPUs already set to %d\n", vmRec.Name, vmRec.Resources.CPUs)
		}
		return nil
	}})
	cmd.AddCommand(&cobra.Command{Use: "memory <name> <size>", Short: "Set memory allocation for a stopped VM", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		memoryMiB, err := state.ParseMemoryMiB(args[1])
		if err != nil {
			return err
		}
		deps, err := loadRuntimeDeps()
		if err != nil {
			return err
		}
		vmRec, changed, err := deps.vm.SetMemory(cmd.Context(), args[0], memoryMiB)
		if err != nil {
			return err
		}
		if outputFormat(cmd) == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": vmRec.Name, "changed": changed, "memoryMiB": vmRec.Resources.MemoryMiB})
		}
		if changed {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "set VM %s memory to %dMiB\n", vmRec.Name, vmRec.Resources.MemoryMiB)
		} else {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "VM %s memory already set to %dMiB\n", vmRec.Name, vmRec.Resources.MemoryMiB)
		}
		return nil
	}})
	return cmd
}

func diskGrowCommand() *cobra.Command {
	return &cobra.Command{Use: "grow <name> [amount]", Short: "Grow a stopped VM disk", Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
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
	}}
}
