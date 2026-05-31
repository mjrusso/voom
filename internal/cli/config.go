package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mjrusso/voom/internal/state"
)

func configCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Inspect and modify VM configuration"}
	show := &cobra.Command{
		Use:   "show <name>",
		Short: "Print the commands to reproduce a VM's configuration",
		Long:  "Print the shell commands that recreate a VM's post-create configuration: its shares, manual forwards, and auto-forward settings. The output is empty for a VM with none of these. ('voom clone' prints the same commands, retargeted at the new VM, so you can match a clone to its source.)",
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
			cmds := replayCommands(vmRec, vmRec.Name)
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": vmRec.Name, "commands": cmds})
			}
			for _, c := range cmds {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), c)
			}
			return nil
		},
	}
	sshPort := &cobra.Command{
		Use:   "ssh-port <name> <port|auto>",
		Short: "Set or reallocate the host SSH port for a stopped VM",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			port := 0
			if args[1] != "auto" {
				var err error
				port, err = state.ParsePort(args[1])
				if err != nil {
					return err
				}
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
			vmRec, changed, err := deps.vm.SetSSHPort(args[0], port)
			if err != nil {
				return err
			}
			if outputFormat(cmd) == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"name": vmRec.Name, "changed": changed, "sshPort": vmRec.Network.SSHPort})
			}
			if changed {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "set VM %s SSH port to %d\n", vmRec.Name, vmRec.Network.SSHPort)
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "VM %s SSH port already set to %d\n", vmRec.Name, vmRec.Network.SSHPort)
			}
			return nil
		},
	}
	cmd.AddCommand(show, sshPort)
	return cmd
}

// replayCommands returns the voom commands that recreate the post-create
// configuration of vm — its shares, manual forwards, and auto-forwarding —
// targeting a VM named target. Commands are emitted in apply order. Resources,
// driver, image, and SSH management port are intentionally omitted: fixed
// create-time settings are already carried by 'voom clone', while clones
// deliberately retain their freshly allocated SSH ports.
func replayCommands(vm *state.VMRecord, target string) []string {
	cmds := []string{}
	for _, s := range vm.Shares {
		line := fmt.Sprintf("voom share add %s %s %s %s", shellQuote(target), shellQuote(s.Tag), shellQuote(s.HostPath), shellQuote(s.GuestPath))
		if s.Readonly {
			line += " --ro"
		}
		cmds = append(cmds, line)
	}
	for _, f := range vm.Network.Forwards {
		line := fmt.Sprintf("voom forward add %s %d --host-port %d", shellQuote(target), f.GuestPort, f.HostPort)
		// Manual forwards default to a 127.0.0.1 bind; only emit --bind when the
		// VM was given a different host bind address.
		if f.Bind != "" && f.Bind != "127.0.0.1" {
			line += " --bind " + shellQuote(f.Bind)
		}
		cmds = append(cmds, line)
	}
	if vm.Network.AutoForward {
		line := fmt.Sprintf("voom forward auto enable %s", shellQuote(target))
		if vm.Network.AutoForwardHostOffset != 0 {
			line += fmt.Sprintf(" --offset %d", vm.Network.AutoForwardHostOffset)
		}
		cmds = append(cmds, line)
	}
	return cmds
}

// shellQuote returns s unchanged when it is safe to paste into a POSIX shell
// unquoted, and single-quoted otherwise. VM names, tags, and bind addresses are
// always safe; host and guest paths may contain spaces or shell metacharacters.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\r'\"\\$`&|;<>(){}[]*?#~!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
