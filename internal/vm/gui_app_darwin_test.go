package vm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/buildinfo"
	"github.com/mjrusso/voom/internal/process"
)

func TestGUIAppMenus(t *testing.T) {
	if os.Getenv("VOOM_TEST_GUI") != "1" {
		t.Skip("set VOOM_TEST_GUI=1 in a logged-in MacOS session with Nix and Apple's SDK")
	}
	root := filepath.Join("..", "..")
	command := exec.Command("nix", "build", "path:.#vfkit-source", "--no-link", "--print-out-paths")
	command.Dir = root
	command.Stderr = os.Stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("prepare vfkit source: %v", err)
	}
	vendor := filepath.Join(strings.TrimSpace(string(output)), "vendor", "github.com", "Code-Hex", "vz", "v3")
	dir := t.TempDir()
	source := filepath.Join(dir, "vfkit")
	output, err = exec.Command("/usr/bin/xcrun", "--sdk", "macosx", "clang", "-fno-objc-arc", "-fblocks",
		"-framework", "Cocoa", "-framework", "Virtualization", "-I", vendor,
		filepath.Join(root, "native", "macos-menus", "main.m"), filepath.Join(vendor, "virtualization_view.m"), "-o", source).CombinedOutput()
	if err != nil {
		t.Fatalf("compile menu probe: %v\n%s", err, output)
	}
	output, err = exec.Command("/usr/bin/codesign", "--force", "--sign", "-", source).CombinedOutput()
	if err != nil {
		t.Fatalf("sign menu probe: %v\n%s", err, output)
	}
	output, err = exec.Command(source).CombinedOutput()
	if err != nil {
		t.Fatalf("unbranded menus: %v\n%s", err, output)
	}
	executable, err := buildGUIApp(context.Background(), source, filepath.Join(dir, "display.app"), "menu-probe", "io.github.mjrusso.voom.menu-probe")
	if err != nil {
		t.Fatal(err)
	}
	output, err = exec.Command(executable, buildinfo.Version).CombinedOutput()
	if err != nil {
		t.Fatalf("Voom menus: %v\n%s", err, output)
	}
}

func TestGUIAppRegistersPerVMNames(t *testing.T) {
	if os.Getenv("VOOM_TEST_GUI") != "1" {
		t.Skip("set VOOM_TEST_GUI=1 in a logged-in MacOS session with Apple's SDK")
	}
	dir := t.TempDir()
	root := filepath.Join("..", "..")
	fixture := filepath.Join(root, "native", "macos-app-name")
	source := filepath.Join(dir, "vfkit")
	output, err := exec.Command("/usr/bin/xcrun", "--sdk", "macosx", "clang", "-fno-objc-arc", "-Wall", "-Wextra", "-Werror", "-framework", "Cocoa", filepath.Join(fixture, "main.m"), "-o", source).CombinedOutput()
	if err != nil {
		t.Fatalf("compile AppKit probe: %v\n%s", err, output)
	}
	output, err = exec.Command("/usr/bin/codesign", "--force", "--sign", "-", "--entitlements", filepath.Join(fixture, "probe.entitlements"), source).CombinedOutput()
	if err != nil {
		t.Fatalf("sign AppKit probe: %v\n%s", err, output)
	}
	check := filepath.Join(root, "scripts", "check-macos-app-name.sh")
	baseline := launchGUIAppProbe(t, source, "baseline")
	output, err = exec.Command("bash", check, strconv.Itoa(baseline), "baseline").CombinedOutput()
	if err == nil || !strings.Contains(string(output), `has registered app name "vfkit"`) {
		t.Fatalf("setting the process name alone should leave the registered app name as vfkit: %v\n%s", err, output)
	}
	type instance struct {
		pid  int
		name string
	}
	var instances []instance
	for _, name := range []string{"first", "second"} {
		bundle := filepath.Join(dir, name, "display.app")
		executable, err := buildGUIApp(context.Background(), source, bundle, name, "io.github.mjrusso.voom.probe."+name)
		if err != nil {
			t.Fatal(err)
		}
		output, err := exec.Command("/usr/bin/osascript", "-l", "JavaScript", "-e", `
ObjC.import('AppKit');
function run(argv) {
    const bundle = $.NSBundle.bundleWithPath(argv[0]);
    const name = ObjC.unwrap(bundle.objectForInfoDictionaryKey('CFBundleIconFile'));
    const image = $.NSImage.alloc.initWithContentsOfFile(argv[0] + '/Contents/Resources/' + name);
    if (!image.isValid) throw new Error('MacOS could not decode the bundled app icon');
}`, bundle).CombinedOutput()
		if err != nil {
			t.Fatalf("decode app icon: %v\n%s", err, output)
		}
		output, err = exec.Command("/usr/bin/codesign", "--verify", "--strict", bundle).CombinedOutput()
		if err != nil {
			t.Fatalf("verify bundle: %v\n%s", err, output)
		}
		output, err = exec.Command("/usr/bin/codesign", "--display", "--entitlements", ":-", bundle).CombinedOutput()
		if err != nil || !strings.Contains(string(output), "com.apple.security.virtualization") {
			t.Fatalf("bundle lost virtualization entitlement: %v\n%s", err, output)
		}
		instances = append(instances, instance{launchGUIAppProbe(t, executable, name), name})
	}
	for _, instance := range instances {
		output, err := exec.Command("bash", check, strconv.Itoa(instance.pid), instance.name).CombinedOutput()
		if err != nil {
			t.Fatalf("registered app name: %v\n%s", err, output)
		}
	}
	bundle := filepath.Join(dir, "renamed", "display.app")
	for _, name := range []string{"before-rename", "after-rename"} {
		t.Run(name, func(t *testing.T) {
			executable, err := buildGUIApp(context.Background(), source, bundle, name, "io.github.mjrusso.voom.probe.renamed")
			if err != nil {
				t.Fatal(err)
			}
			pid := launchGUIAppProbe(t, executable, name)
			output, err := exec.Command("bash", check, strconv.Itoa(pid), name).CombinedOutput()
			if err != nil {
				t.Fatalf("registered app name after replacing bundle: %v\n%s", err, output)
			}
		})
	}
}

func launchGUIAppProbe(t *testing.T, executable, name string) int {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "probe.log")
	record := filepath.Join(dir, "vm.process.json")
	if err := process.StartRecorded(executable, []string{"Voom: " + name}, log, record); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.StopRecorded(record, "vfkit", time.Second); err != nil {
			t.Error(err)
		}
	})
	if err := process.WaitFor(func() bool {
		data, _ := os.ReadFile(log)
		return strings.Contains(string(data), "READY")
	}, 10*time.Second); err != nil {
		data, _ := os.ReadFile(log)
		t.Fatalf("AppKit probe did not become ready: %v\n%s", err, data)
	}
	pid, valid := process.ValidRecord(record, "vfkit")
	if !valid {
		t.Fatal("bundled process was not recognized as vfkit")
	}
	return pid
}
