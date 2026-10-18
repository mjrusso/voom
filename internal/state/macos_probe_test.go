package state

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMacOSCloneProbeCopiesIndependentCompleteBundle(t *testing.T) {
	source := macosBundle(t)
	destination := filepath.Join(t.TempDir(), "probe")
	report, err := PrepareMacOSCloneProbe(context.Background(), source, destination, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.FreshIdentity || report.SourceIdentitySHA256 != report.CloneIdentitySHA256 || report.HardwareModelSHA256 == "" || report.AuxiliaryStorageSHA256 == "" {
		t.Fatalf("unexpected control report: %+v", report)
	}
	for _, name := range macosBundleFiles() {
		original, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		copied, err := os.ReadFile(filepath.Join(destination, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(original) != string(copied) {
			t.Fatalf("probe changed %s", name)
		}
	}
	for _, name := range []string{"disk.raw", "AuxiliaryStorage"} {
		if err := os.WriteFile(filepath.Join(destination, name), []byte("guest write"), 0o600); err != nil {
			t.Fatal(err)
		}
		original, err := os.ReadFile(filepath.Join(source, name))
		if err != nil || string(original) != name {
			t.Fatalf("probe write changed source %s: %q %v", name, original, err)
		}
	}
	var saved MacOSCloneProbe
	if err := ReadJSON(filepath.Join(destination, "identity-probe.json"), &saved); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(destination)
	if err != nil {
		t.Fatal(err)
	}
	if saved.CloneIdentitySHA256 != report.CloneIdentitySHA256 || saved.Destination != canonical {
		t.Fatalf("wrong saved evidence: %+v", saved)
	}
}

func TestMacOSCloneProbePreservesExistingDestinationAndCleansCancelledCopy(t *testing.T) {
	source := macosBundle(t)
	destination := t.TempDir()
	marker := filepath.Join(destination, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareMacOSCloneProbe(context.Background(), source, destination, false); !os.IsExist(err) {
		t.Fatalf("existing destination accepted: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("existing destination was removed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	destination = filepath.Join(t.TempDir(), "cancelled")
	if _, err := PrepareMacOSCloneProbe(ctx, source, destination, false); err == nil {
		t.Fatal("cancelled probe succeeded")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("cancelled probe left files: %v", err)
	}
}

func TestMacOSCloneProbeRejectsDestinationInsideSource(t *testing.T) {
	source := macosBundle(t)
	alias := filepath.Join(t.TempDir(), "source-alias")
	if err := os.Symlink(source, alias); err != nil {
		t.Fatal(err)
	}
	for _, parent := range []string{source, alias} {
		if _, err := PrepareMacOSCloneProbe(context.Background(), source, filepath.Join(parent, "probe"), false); err == nil {
			t.Fatal("probe was allowed inside the golden bundle")
		}
	}
	if _, err := os.Stat(filepath.Join(source, "probe")); !os.IsNotExist(err) {
		t.Fatalf("golden bundle was modified: %v", err)
	}
}

func TestFreshMacOSIdentityProbeRequiresSupportedHost(t *testing.T) {
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		t.Skip("native identity generation is exercised with a real bundle on the Mac")
	}
	destination := filepath.Join(t.TempDir(), "probe")
	if _, err := PrepareMacOSCloneProbe(context.Background(), macosBundle(t), destination, true); err == nil {
		t.Fatal("native identity generation accepted an unsupported host")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("unsupported probe created files: %v", err)
	}
}
