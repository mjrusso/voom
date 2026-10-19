// Package bootstrap builds the NoCloud cloud-init seed image used to first-boot a voom VM.
package bootstrap

import (
	_ "embed"

	"github.com/mjrusso/voom/internal/control"
)

//go:embed helpers/voom-portfwd
var legacyPortFwd string

//go:embed helpers/voom-mount-shares
var legacyMountShares string

//go:embed helpers/run-voom.mount
var legacyControlMount string

//go:embed helpers/voom-portfwd.service
var legacyPortFwdService string

//go:embed helpers/voom-mount-shares.service
var legacyMountSharesService string

type helperFile struct {
	Path        string
	Permissions string
	Content     string
}

func helperFiles() []helperFile {
	return []helperFile{
		{Path: "/etc/modules-load.d/voom.conf", Permissions: "0644", Content: "virtiofs\nvmw_vsock_virtio_transport\nqemu_fw_cfg\n"},
		{Path: "/usr/local/sbin/voom_control.py", Permissions: "0644", Content: control.Agent},
		{Path: "/usr/local/sbin/voom-guest", Permissions: "0755", Content: "#!/usr/bin/env python3\n" + control.LinuxAgent},
		{Path: "/etc/systemd/system/voom-guest.service", Permissions: "0644", Content: `[Unit]
Description=Voom vsock guest control
After=systemd-modules-load.service
[Service]
ExecStart=/usr/local/sbin/voom-guest
Restart=always
RestartSec=2
[Install]
WantedBy=multi-user.target
`},
	}
}

var helperEnabledUnits = []string{"voom-guest.service"}
var helperPackages = []string{"python3", "iproute2", "util-linux"}

func legacyHelperFiles() []helperFile {
	return []helperFile{
		{Path: "/etc/modules-load.d/voom.conf", Permissions: "0644", Content: "virtiofs\n"},
		{Path: "/usr/local/sbin/voom-portfwd", Permissions: "0755", Content: legacyPortFwd},
		{Path: "/usr/local/sbin/voom-mount-shares", Permissions: "0755", Content: legacyMountShares},
		{Path: "/etc/systemd/system/run-voom.mount", Permissions: "0644", Content: legacyControlMount},
		{Path: "/etc/systemd/system/voom-portfwd.service", Permissions: "0644", Content: legacyPortFwdService},
		{Path: "/etc/systemd/system/voom-mount-shares.service", Permissions: "0644", Content: legacyMountSharesService},
	}
}
