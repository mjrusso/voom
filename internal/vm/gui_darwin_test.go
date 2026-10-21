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
		localized := readGUIAppPlist(t, filepath.Join(bundle, "Contents", "Resources", "en.lproj", "InfoPlist.strings"))
		for _, key := range []string{"CFBundleName", "CFBundleDisplayName"} {
			if localized[key] != "Voom: "+name {
				t.Fatalf("localized %s does not contain the current VM name", key)
			}
		}
	}
}
