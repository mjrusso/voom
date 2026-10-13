package cli

import (
	"errors"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mjrusso/voom/internal/host"
	"github.com/mjrusso/voom/internal/state"
)

func completeVMNames(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	index, err := state.ReadIndex(host.ResolvePaths().State)
	return completeStoredNames(index.VMs, toComplete, err)
}

func completeImageNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeImageFlag(cmd, args, toComplete)
}

func completeImageFlag(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	index, err := state.ReadIndex(host.ResolvePaths().State)
	return completeStoredNames(index.Images, toComplete, err)
}

func completeStoredNames(names map[string]string, prefix string, err error) ([]string, cobra.ShellCompDirective) {
	if errors.Is(err, os.ErrNotExist) {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if err != nil {
		return nil, cobra.ShellCompDirectiveError | cobra.ShellCompDirectiveNoFileComp
	}
	matches := []string{}
	for name := range names {
		if strings.HasPrefix(name, prefix) {
			matches = append(matches, name)
		}
	}
	sort.Strings(matches)
	return matches, cobra.ShellCompDirectiveNoFileComp
}

func completeImageImport(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 1 {
		return nil, cobra.ShellCompDirectiveDefault
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func completeShareAdd(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 2 {
		return nil, cobra.ShellCompDirectiveDefault
	}
	return completeVMNames(cmd, args, toComplete)
}
