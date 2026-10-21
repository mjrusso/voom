// Package buildinfo holds metadata shared by Voom's CLI and graphical apps.
package buildinfo

// Version, Commit, and Date are set at link time with -ldflags -X.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)
