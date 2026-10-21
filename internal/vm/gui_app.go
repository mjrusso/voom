package vm

import (
	"context"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mjrusso/voom/assets"
	"github.com/mjrusso/voom/internal/buildinfo"
	"github.com/mjrusso/voom/internal/state"
)

const guiAppInfoPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>vfkit</string>
<key>CFBundleIconFile</key><string>voom.icns</string>
<key>CFBundleIdentifier</key><string>%[2]s</string>
<key>CFBundleName</key><string>%[1]s</string>
<key>CFBundleDisplayName</key><string>%[1]s</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleVersion</key><string>1</string>
<key>CFBundleShortVersionString</key><string>%[3]s</string>
<key>VoomAppName</key><string>Voom</string>
<key>VoomVMName</key><string>%[4]s</string>
<key>VoomProjectURL</key><string>https://github.com/mjrusso/voom</string>
<key>VoomHelpURL</key><string>https://github.com/mjrusso/voom/blob/main/docs/macos-guests.md</string>
<key>VoomLicensesURL</key><string>https://github.com/mjrusso/voom/blob/main/docs/third-party-licenses.md</string>
<key>NSHighResolutionCapable</key><true/>
<key>CFBundleDevelopmentRegion</key><string>en</string>
<key>LSHasLocalizedDisplayName</key><true/>
</dict></plist>
`

const guiAppLocalizedNames = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleName</key><string>%[1]s</string>
<key>CFBundleDisplayName</key><string>%[1]s</string>
</dict></plist>
`

// The previous instance must have exited before replacing its bundle.
func buildGUIApp(ctx context.Context, source, bundlePath, vmName, bundleID string) (string, error) {
	executable, err := writeGUIApp(ctx, source, bundlePath, vmName, bundleID)
	if err != nil {
		return "", err
	}
	output, err := exec.CommandContext(ctx, "/usr/bin/codesign", "--force", "--sign", "-", "--preserve-metadata=entitlements", bundlePath).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("sign MacOS display app: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return executable, nil
}

func writeGUIApp(ctx context.Context, source, bundlePath, vmName, bundleID string) (string, error) {
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", err
	}
	if err := os.RemoveAll(bundlePath); err != nil {
		return "", err
	}
	executable := filepath.Join(bundlePath, "Contents", "MacOS", "vfkit")
	// A symlink loses the bundle identity; a hard link would let signing modify the source.
	if err := state.CopyFile(ctx, resolved, executable); err != nil {
		return "", err
	}
	if err := os.Chmod(executable, 0o755); err != nil {
		return "", err
	}
	// The Dock uses localized names only when the base names match the bundle filename.
	baseName := strings.TrimSuffix(filepath.Base(bundlePath), ".app")
	plist := fmt.Sprintf(guiAppInfoPlist, html.EscapeString(baseName), html.EscapeString(bundleID), html.EscapeString(buildinfo.Version), html.EscapeString(vmName))
	if err := os.WriteFile(filepath.Join(bundlePath, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		return "", err
	}
	resourcesDir := filepath.Join(bundlePath, "Contents", "Resources")
	localizedDir := filepath.Join(resourcesDir, "en.lproj")
	if err := os.MkdirAll(localizedDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(resourcesDir, "voom.icns"), assets.MacOSIcon, 0o644); err != nil {
		return "", err
	}
	localizedNames := fmt.Sprintf(guiAppLocalizedNames, html.EscapeString("Voom: "+vmName))
	if err := os.WriteFile(filepath.Join(localizedDir, "InfoPlist.strings"), []byte(localizedNames), 0o644); err != nil {
		return "", err
	}
	return executable, nil
}
