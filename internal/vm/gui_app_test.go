package vm

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGUIAppCopiesExecutableAndReplacesMetadata(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, []byte("original executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "vfkit")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(dir, "display app", "Voom-dev.app")
	for _, name := range []string{"Voom: old", `Voom: dev <&> "test"`, "Voom: " + strings.Repeat("a", 247)} {
		executable, err := writeGUIApp(context.Background(), link, bundle, name, "io.github.mjrusso.voom.vm.test")
		if err != nil {
			t.Fatal(err)
		}
		values := readGUIAppPlist(t, filepath.Join(bundle, "Contents", "Info.plist"))
		for key, want := range map[string]string{
			"CFBundleName": "Voom-dev", "CFBundleDisplayName": "Voom-dev",
			"CFBundleDevelopmentRegion": "en",
			"CFBundleIdentifier":        "io.github.mjrusso.voom.vm.test", "CFBundleExecutable": "vfkit",
		} {
			if values[key] != want {
				t.Fatalf("%s = %q, want %q", key, values[key], want)
			}
		}
		localized := readGUIAppPlist(t, filepath.Join(bundle, "Contents", "Resources", "en.lproj", "InfoPlist.strings"))
		for _, key := range []string{"CFBundleName", "CFBundleDisplayName"} {
			if localized[key] != name {
				t.Fatalf("localized %s = %q, want %q", key, localized[key], name)
			}
		}
		info, err := os.Lstat(executable)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 {
			t.Fatalf("bundled executable: %v, %v", info, err)
		}
		if err := os.WriteFile(executable, []byte("modified signature"), 0o755); err != nil {
			t.Fatal(err)
		}
		original, err := os.ReadFile(source)
		if err != nil || string(original) != "original executable" {
			t.Fatalf("source changed when the bundle was modified: %q, %v", original, err)
		}
	}
}

func readGUIAppPlist(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var plist struct {
		Dict struct {
			Entries []struct {
				XMLName xml.Name
				Value   string `xml:",chardata"`
			} `xml:",any"`
		} `xml:"dict"`
	}
	if err := xml.Unmarshal(data, &plist); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for i := 0; i+1 < len(plist.Dict.Entries); i += 2 {
		key, value := plist.Dict.Entries[i], plist.Dict.Entries[i+1]
		if key.XMLName.Local == "key" && value.XMLName.Local == "string" {
			values[key.Value] = value.Value
		}
	}
	return values
}
