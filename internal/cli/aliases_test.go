package cli

import "testing"

func TestAliasesResolveToExistingCommands(t *testing.T) {
	for _, tc := range []struct{ alias, command []string }{
		{[]string{"shutdown"}, []string{"stop"}},
		{[]string{"reboot"}, []string{"restart"}},
		{[]string{"inspect"}, []string{"info"}},
		{[]string{"image", "info"}, []string{"image", "inspect"}},
		{[]string{"delete"}, []string{"remove"}},
		{[]string{"image", "delete"}, []string{"image", "remove"}},
		{[]string{"share", "delete"}, []string{"share", "remove"}},
		{[]string{"forward", "delete"}, []string{"forward", "remove"}},
		{[]string{"usb", "delete"}, []string{"usb", "remove"}},
		{[]string{"pci", "delete"}, []string{"pci", "remove"}},
		{[]string{"gpu", "delete"}, []string{"pci", "remove"}},
	} {
		root := NewRootCommand()
		alias, _, err := root.Find(tc.alias)
		if err != nil {
			t.Fatal(err)
		}
		command, _, err := root.Find(tc.command)
		if err != nil {
			t.Fatal(err)
		}
		if alias != command {
			t.Fatalf("%v resolved to %s, want %s", tc.alias, alias.CommandPath(), command.CommandPath())
		}
	}
}
