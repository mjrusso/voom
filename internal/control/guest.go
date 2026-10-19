// Package control defines the guest protocol and host vsock endpoints.
package control

import _ "embed"

// Agent is the protocol module installed unchanged in Linux and Darwin guests.
//
//go:embed agent.py
var Agent string

// LinuxAgent requires Agent to be installed alongside it as voom_control.py.
//
//go:embed guest_linux.py
var LinuxAgent string
