package vm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mjrusso/voom/internal/state"
)

func TestPrepareGUIAppSupportsLongNamesAndRename(t *testing.T) {
	st, _ := newTestStore(t)
	vm := &state.VMRecord{ID: "mac", MacOS: &state.MacOSPlatform{}}
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	manager := New(st)
	for _, name := range []string{strings.Repeat("a", 247), "renamed"} {
		if err := state.ValidateName("VM", name); err != nil {
			t.Fatal(err)
		}
		vm.Name = name
		executable, err := manager.prepareGUIApp(context.Background(), vm, source)
		if err != nil {
			t.Fatal(err)
		}
		bundle := st.Runtime(vm).GUIApp()
		if executable != filepath.Join(bundle, "Contents", "MacOS", "vfkit") {
			t.Fatalf("unexpected executable path: %s", executable)
		}
		plist, err := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(plist), "<string>Voom: "+name+"</string>") {
			t.Fatal("bundle does not contain the current VM name")
		}
	}
}
