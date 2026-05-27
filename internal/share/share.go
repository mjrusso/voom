package share

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Decl is a validated share declaration mapping a host directory to a guest mount path.
type Decl struct {
	Tag       string `json:"tag"`
	HostPath  string `json:"hostPath"`
	GuestPath string `json:"guestPath"`
	Readonly  bool   `json:"readonly"`
}

// Runtime captures the per-VM virtiofsd socket, pidfile, and paths derived from a share.
type Runtime struct {
	Tag       string
	Sock      string
	Pidfile   string
	HostPath  string
	GuestPath string
	Readonly  bool
	LogKind   string
}

var tagRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// NewDecl validates the inputs and returns a Decl with an absolute host path.
func NewDecl(tag, hostPath, guestPath, reservedTag string, readonly bool) (Decl, error) {
	if !tagRE.MatchString(tag) || tag == reservedTag {
		return Decl{}, fmt.Errorf("invalid or reserved share tag %q", tag)
	}
	if !filepath.IsAbs(guestPath) {
		return Decl{}, fmt.Errorf("guest path must be absolute: %s", guestPath)
	}
	info, err := os.Stat(hostPath)
	if err != nil || !info.IsDir() {
		return Decl{}, fmt.Errorf("host path is not a directory: %s", hostPath)
	}
	hostPath, _ = filepath.Abs(hostPath)
	return Decl{Tag: tag, HostPath: hostPath, GuestPath: guestPath, Readonly: readonly}, nil
}

// RuntimeForVM builds a Runtime for the given share, placing its socket and pidfile under runtimeVMDir.
func RuntimeForVM(runtimeVMDir string, sh Decl) (Runtime, error) {
	sock := filepath.Join(runtimeVMDir, "virtiofs-"+sh.Tag+".sock")
	if err := ValidateUnixSocketPath("share "+sh.Tag, sock); err != nil {
		return Runtime{}, err
	}
	return Runtime{
		Tag:       sh.Tag,
		Sock:      sock,
		Pidfile:   filepath.Join(runtimeVMDir, "virtiofs-"+sh.Tag+".pid"),
		HostPath:  sh.HostPath,
		GuestPath: sh.GuestPath,
		Readonly:  sh.Readonly,
		LogKind:   "virtiofs-" + sh.Tag,
	}, nil
}

// ControlRuntime builds a Runtime for the reserved control share that exposes controlDir under the given tag.
func ControlRuntime(runtimeVMDir, controlDir, tag string) (Runtime, error) {
	sock := filepath.Join(runtimeVMDir, "virtiofs-"+tag+".sock")
	if err := ValidateUnixSocketPath("control share", sock); err != nil {
		return Runtime{}, err
	}
	return Runtime{
		Tag:      tag,
		Sock:     sock,
		Pidfile:  filepath.Join(runtimeVMDir, "virtiofs-"+tag+".pid"),
		HostPath: controlDir,
		LogKind:  "virtiofs-" + tag,
	}, nil
}

// VirtiofsdArgs returns the command-line arguments for launching virtiofsd for the given Runtime.
func VirtiofsdArgs(sh Runtime) []string {
	args := []string{
		"--shared-dir", sh.HostPath,
		"--socket-path", sh.Sock,
		"--cache", "auto",
		"--sandbox", "none",
		"--seccomp", "none",
		"--inode-file-handles=never",
	}
	if sh.Readonly {
		args = append(args, "--readonly")
	}
	return args
}

// ValidateUnixSocketPath returns an error if sock exceeds the Linux Unix socket path length limit.
func ValidateUnixSocketPath(label, sock string) error {
	if runtime.GOOS == "linux" && len(sock) >= 108 {
		return fmt.Errorf("%s socket path is too long for Linux Unix sockets: %s; set VOOM_RUNTIME_DIR to a shorter path", label, sock)
	}
	return nil
}

// GuestMountScript returns a shell script that mounts the share's virtiofs tag at its guest path.
func GuestMountScript(sh Runtime) string {
	opts := ""
	if sh.Readonly {
		opts = " -o ro"
	}
	return strings.Join([]string{
		"set -eu",
		"target=" + shellQuote(sh.GuestPath),
		"tag=" + shellQuote(sh.Tag),
		"nsenter --mount=/proc/1/ns/mnt -- mkdir -p \"$target\"",
		"if nsenter --mount=/proc/1/ns/mnt -- mountpoint -q \"$target\"; then exit 0; fi",
		"nsenter --mount=/proc/1/ns/mnt -- mount -t virtiofs" + opts + " \"$tag\" \"$target\"",
	}, "\n") + "\n"
}

// GuestUnmountScript returns a shell script that unmounts the share's guest path if mounted.
func GuestUnmountScript(sh Decl) string {
	return strings.Join([]string{
		"set -u",
		"target=" + shellQuote(sh.GuestPath),
		"if nsenter --mount=/proc/1/ns/mnt -- mountpoint -q \"$target\"; then",
		"  nsenter --mount=/proc/1/ns/mnt -- umount \"$target\"",
		"fi",
	}, "\n") + "\n"
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
