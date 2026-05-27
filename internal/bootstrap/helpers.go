// Package bootstrap builds the NoCloud cloud-init seed image used to first-boot a voom VM.
package bootstrap

import _ "embed"

//go:embed helpers/voom-portfwd
var helperPortFwdScript string

//go:embed helpers/voom-mount-shares
var helperMountSharesScript string

//go:embed helpers/run-voom.mount
var helperRunVoomMount string

//go:embed helpers/voom-portfwd.service
var helperPortFwdService string

//go:embed helpers/voom-mount-shares.service
var helperMountSharesService string

type helperFile struct {
	Path        string
	Permissions string
	Content     string
}

func helperFiles() []helperFile {
	return []helperFile{
		{Path: "/etc/modules-load.d/voom.conf", Permissions: "0644", Content: "virtiofs\n"},
		{Path: "/usr/local/sbin/voom-portfwd", Permissions: "0755", Content: helperPortFwdScript},
		{Path: "/usr/local/sbin/voom-mount-shares", Permissions: "0755", Content: helperMountSharesScript},
		{Path: "/etc/systemd/system/run-voom.mount", Permissions: "0644", Content: helperRunVoomMount},
		{Path: "/etc/systemd/system/voom-portfwd.service", Permissions: "0644", Content: helperPortFwdService},
		{Path: "/etc/systemd/system/voom-mount-shares.service", Permissions: "0644", Content: helperMountSharesService},
	}
}

var helperEnabledUnits = []string{"run-voom.mount", "voom-portfwd.service", "voom-mount-shares.service"}

var helperPackages = []string{"jq", "gawk", "iproute2"}
