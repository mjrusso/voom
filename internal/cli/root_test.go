package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/host"
	"github.com/mjrusso/voom/internal/share"
	"github.com/mjrusso/voom/internal/state"
)

func TestVersionJSON(t *testing.T) {
	var out bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--output", "json", "version"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var v VersionInfo
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Version == "" || v.Commit == "" || v.Date == "" {
		t.Fatalf("incomplete version: %#v", v)
	}
}

func TestUnsupportedOutputRejected(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--output", "xml", "version"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unsupported output format") {
		t.Fatalf("expected unsupported output error, got %v", err)
	}
}

func TestVersionText(t *testing.T) {
	out := runCmd(t, "version")
	if !strings.Contains(out, "voom ") || !strings.Contains(out, "commit:") || !strings.Contains(out, "built:") {
		t.Fatalf("unexpected version text:\n%s", out)
	}
}

func TestVersionFlagMatchesSubcommand(t *testing.T) {
	if flag, sub := runCmd(t, "--version"), runCmd(t, "version"); flag != sub {
		t.Fatalf("--version output differs from version subcommand:\n--version:\n%s\nversion:\n%s", flag, sub)
	}
}

func TestValidationAndSizeParsing(t *testing.T) {
	if err := state.ValidateName("VM", "scratch_1"); err != nil {
		t.Fatal(err)
	}
	if err := state.ValidateName("VM", "-bad"); err == nil {
		t.Fatal("expected invalid name")
	}
	if _, err := state.ParseMemoryMiB("4096MiB"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.ParseMemoryMiB("4096"); err == nil {
		t.Fatal("expected bare memory size rejection")
	}
	if _, err := state.ParseMemoryMiB(strings.Repeat("9", 1024) + "GiB"); err == nil {
		t.Fatal("expected size overflow rejection")
	}
	if _, err := forward.NormalizeBind("localhost"); err == nil {
		t.Fatal("expected localhost bind rejection")
	}
	if !forward.BindsConflict("0.0.0.0", "127.0.0.1") {
		t.Fatal("expected wildcard IPv4 conflict")
	}
	caps := state.ParseCapabilities(map[string]any{"capabilities": map[string]any{"metadataDisk": true, "guestPortReport": true}})
	if !caps.MetadataDisk || !caps.GuestPortReport || caps.ControlShare {
		t.Fatalf("unexpected parsed capabilities: %#v", caps)
	}
	caps = state.ParseCapabilities(map[string]any{"metadataDisk": true, "controlShare": true, "guestPortReport": true, "guestShareMount": true, "nixosSwitch": true})
	if !caps.MetadataDisk || !caps.ControlShare || !caps.GuestPortReport || !caps.GuestShareMount || !caps.NixosSwitch {
		t.Fatalf("unexpected top-level parsed capabilities: %#v", caps)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	if got := host.RuntimeDir(1234); got != filepath.Join(os.TempDir(), "voom-1234") {
		t.Fatalf("unexpected fallback runtime dir: %s", got)
	}
}

func TestCommandSurfaceHelpAndArgValidation(t *testing.T) {
	helpCommands := [][]string{
		{"--help"},
		{"debug", "paths", "--help"},
		{"guest", "ports", "--help"},
		{"image", "list", "--help"},
		{"image", "inspect", "--help"},
		{"image", "import", "--help"},
		{"image", "rm", "--help"},
		{"create", "--help"},
		{"start", "--help"},
		{"stop", "--help"},
		{"restart", "--help"},
		{"ssh", "--help"},
		{"ssh-config", "--help"},
		{"console", "--help"},
		{"logs", "--help"},
		{"info", "--help"},
		{"list", "--help"},
		{"rename", "--help"},
		{"rm", "--help"},
		{"disk", "reset", "--help"},
		{"resources", "--help"},
		{"resources", "cpus", "--help"},
		{"resources", "disk", "--help"},
		{"resources", "disk", "grow", "--help"},
		{"resources", "memory", "--help"},
		{"config", "--help"},
		{"config", "show", "--help"},
		{"config", "ssh-port", "--help"},
		{"forward", "add", "--help"},
		{"forward", "rm", "--help"},
		{"forward", "ls", "--help"},
		{"forward", "auto", "enable", "--help"},
		{"forward", "auto", "disable", "--help"},
		{"forward", "auto", "offset", "--help"},
		{"forward", "discover", "--help"},
		{"share", "add", "--help"},
		{"share", "rm", "--help"},
		{"share", "ls", "--help"},
		{"nixos", "switch", "--help"},
		{"doctor", "--help"},
		{"version", "--help"},
	}
	for _, args := range helpCommands {
		if out := runCmd(t, args...); !strings.Contains(out, "Usage:") {
			t.Fatalf("voom %s help missing usage:\n%s", strings.Join(args, " "), out)
		}
	}
	if err := runCmdErr("create"); err == nil {
		t.Fatal("expected create arg validation error")
	}
	if err := runCmdErr("forward", "add", "scratch", "70000"); err == nil || !strings.Contains(err.Error(), "invalid TCP port") {
		t.Fatalf("expected port validation error, got %v", err)
	}
	if err := runCmdErr("forward", "add", "scratch", "8080", "--host-port", "70000"); err == nil || !strings.Contains(err.Error(), "invalid TCP port") {
		t.Fatalf("expected host port validation error, got %v", err)
	}
	if err := runCmdErr("resources", "cpus", "scratch", "0"); err == nil || !strings.Contains(err.Error(), "cpus must be at least 1") {
		t.Fatalf("expected CPU validation error, got %v", err)
	}
	if err := runCmdErr("resources", "memory", "scratch", "512"); err == nil || !strings.Contains(err.Error(), "bare size") {
		t.Fatalf("expected memory validation error, got %v", err)
	}
	if err := runCmdErr("config", "ssh-port", "scratch", "70000"); err == nil || !strings.Contains(err.Error(), "invalid TCP port") {
		t.Fatalf("expected SSH port validation error, got %v", err)
	}
	if err := runCmdErr("share", "add", "scratch", "voom-control", ".", "/mnt/src"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected reserved share tag error, got %v", err)
	}
	if err := runCmdErr("nixos", "switch", "scratch"); err == nil || !strings.Contains(err.Error(), "--flake is required") {
		t.Fatalf("expected flake validation error, got %v", err)
	}
}

func TestImageCreateInspectForwardAndGuestGate(t *testing.T) {
	withHostPortAvailable(t, func(string, int) (bool, string) { return true, "" })
	dir := t.TempDir()
	t.Setenv("VOOM_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("VOOM_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("VOOM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("VOOM_RUNTIME_DIR", filepath.Join(dir, "runtime"))

	format := testImageFormat()
	image := filepath.Join(dir, "golden."+format)
	meta := filepath.Join(dir, "golden."+format+".meta.json")
	if err := os.WriteFile(image, []byte(format), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(meta, []byte(`{"user":"mjrusso","system":"`+host.System()+`","format":"`+format+`","flake_rev":"abc","baked_at":"2026-05-21T00:00:00Z","capabilities":{"controlShare":true,"guestPortReport":true,"guestShareMount":true,"nixosSwitch":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	runCmd(t, "image", "import", "nixos", image, "--meta", meta)
	out := runCmd(t, "image", "inspect", "nixos")
	if !strings.Contains(out, "controlShare=true guestPortReport=true guestShareMount=true nixosSwitch=true") {
		t.Fatalf("missing capability output:\n%s", out)
	}
	if !strings.Contains(out, "kernelPath: ") || !strings.Contains(out, "kernelCmdline: ") {
		t.Fatalf("missing kernel metadata output:\n%s", out)
	}
	out = runCmd(t, "--output", "json", "create", "scratch", "--image", "nixos", "--memory", "512MiB")
	var created struct {
		CPUs      int `json:"cpus"`
		MemoryMiB int `json:"memoryMiB"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatal(err)
	}
	if created.CPUs != 4 || created.MemoryMiB != 512 {
		t.Fatalf("bad create resource JSON: %#v", created)
	}
	out = runCmd(t, "--output", "json", "resources", "cpus", "scratch", "6")
	var cpusChanged struct {
		Changed bool `json:"changed"`
		CPUs    int  `json:"cpus"`
	}
	if err := json.Unmarshal([]byte(out), &cpusChanged); err != nil {
		t.Fatal(err)
	}
	if !cpusChanged.Changed || cpusChanged.CPUs != 6 {
		t.Fatalf("bad CPU resource JSON: %#v", cpusChanged)
	}
	out = runCmd(t, "--output", "json", "resources", "memory", "scratch", "1GiB")
	var memoryChanged struct {
		Changed   bool `json:"changed"`
		MemoryMiB int  `json:"memoryMiB"`
	}
	if err := json.Unmarshal([]byte(out), &memoryChanged); err != nil {
		t.Fatal(err)
	}
	if !memoryChanged.Changed || memoryChanged.MemoryMiB != 1024 {
		t.Fatalf("bad memory resource JSON: %#v", memoryChanged)
	}
	out = runCmd(t, "--output", "json", "resources", "memory", "scratch", "1GiB")
	if err := json.Unmarshal([]byte(out), &memoryChanged); err != nil {
		t.Fatal(err)
	}
	if memoryChanged.Changed || memoryChanged.MemoryMiB != 1024 {
		t.Fatalf("bad idempotent memory resource JSON: %#v", memoryChanged)
	}
	out = runCmd(t, "--output", "json", "config", "ssh-port", "scratch", "2250")
	var sshPortChanged struct {
		Changed bool `json:"changed"`
		SSHPort int  `json:"sshPort"`
	}
	if err := json.Unmarshal([]byte(out), &sshPortChanged); err != nil {
		t.Fatal(err)
	}
	if !sshPortChanged.Changed || sshPortChanged.SSHPort != 2250 {
		t.Fatalf("bad SSH-port JSON: %#v", sshPortChanged)
	}
	out = runCmd(t, "--output", "json", "config", "ssh-port", "scratch", "2250")
	if err := json.Unmarshal([]byte(out), &sshPortChanged); err != nil {
		t.Fatal(err)
	}
	if sshPortChanged.Changed || sshPortChanged.SSHPort != 2250 {
		t.Fatalf("bad idempotent SSH-port JSON: %#v", sshPortChanged)
	}
	out = runCmd(t, "--output", "json", "config", "ssh-port", "scratch", "auto")
	if err := json.Unmarshal([]byte(out), &sshPortChanged); err != nil {
		t.Fatal(err)
	}
	if !sshPortChanged.Changed || sshPortChanged.SSHPort != state.SSHLow {
		t.Fatalf("bad auto SSH-port JSON: %#v", sshPortChanged)
	}
	st, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	vmRec, err := st.LoadVM("scratch")
	if err != nil {
		t.Fatal(err)
	}
	if vmRec.Access.NixosTargetUser != "mjrusso" {
		t.Fatalf("nixos target user = %q, want imported SSH user", vmRec.Access.NixosTargetUser)
	}
	if vmRec.Resources.CPUs != 6 || vmRec.Resources.MemoryMiB != 1024 {
		t.Fatalf("resources = %#v, want 6 CPUs and 1024MiB", vmRec.Resources)
	}
	out = runCmd(t, "info", "scratch")
	if !strings.Contains(out, "ssh: mjrusso@127.0.0.1:2222") {
		t.Fatalf("unexpected info output:\n%s", out)
	}
	if !strings.Contains(out, "cpus: 6") || !strings.Contains(out, "memory: 1024MiB") {
		t.Fatalf("missing resource output:\n%s", out)
	}
	runCmd(t, "forward", "add", "scratch", "8080", "--host-port", "18080")
	if err := runCmdErr("forward", "add", "scratch", "8081", "--host-port", "18080"); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("expected duplicate forward rejection, got %v", err)
	}
	out = runCmd(t, "forward", "ls", "scratch")
	if !strings.Contains(out, "manual\ttcp\t127.0.0.1:18080") || !strings.Contains(out, "installed=false") {
		t.Fatalf("missing forward row:\n%s", out)
	}
	err = runCmdErr("guest", "ports", "scratch")
	if err == nil || !strings.Contains(err.Error(), "guest port report unavailable") {
		t.Fatalf("expected unavailable guest report, got %v", err)
	}
	err = runCmdErr("forward", "discover", "scratch")
	if err == nil || !strings.Contains(err.Error(), "guest port report unavailable") {
		t.Fatalf("expected unavailable discover report, got %v", err)
	}
	hostShare := filepath.Join(dir, "share")
	if err := os.Mkdir(hostShare, 0o755); err != nil {
		t.Fatal(err)
	}
	runCmd(t, "--output", "json", "share", "add", "scratch", "src", hostShare, "/mnt/src", "--readonly")
	out = runCmd(t, "--output", "json", "share", "ls", "scratch")
	var shares []share.Decl
	if err := json.Unmarshal([]byte(out), &shares); err != nil || len(shares) != 1 || !shares[0].Readonly {
		t.Fatalf("bad share JSON %v: %s", err, out)
	}
	runCmd(t, "share", "rm", "scratch", "src")
	if err := runCmdErr("image", "rm", "nixos"); err == nil || !strings.Contains(err.Error(), "referenced by scratch") {
		t.Fatalf("expected image removal reference block, got %v", err)
	}
	runCmd(t, "--output", "json", "forward", "auto", "enable", "scratch", "--offset", "10000")
	if err := runCmdErr("forward", "auto", "enable", "scratch", "--offset", "-1"); err == nil || !strings.Contains(err.Error(), "non-negative") {
		t.Fatalf("expected negative enable offset rejection, got %v", err)
	}
	runCmd(t, "--output", "json", "forward", "auto", "offset", "scratch", "9000")
	runCmd(t, "--output", "json", "forward", "auto", "disable", "scratch")
	runCmd(t, "--output", "json", "forward", "rm", "scratch", "18080")
	runCmd(t, "rename", "scratch", "renamed")
	runCmd(t, "rm", "renamed", "--force")
	runCmd(t, "--output", "json", "image", "rm", "nixos", "--force")
}

func TestJSONCommandContractsOnFixture(t *testing.T) {
	dir := t.TempDir()
	root := copyFixtureState(t, dir)
	t.Setenv("VOOM_STATE_DIR", root)
	t.Setenv("VOOM_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("VOOM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("VOOM_RUNTIME_DIR", filepath.Join(dir, "runtime"))

	jsonCommands := [][]string{
		{"--output", "json", "version"},
		{"--output", "json", "doctor"},
		{"--output", "json", "debug", "paths"},
		{"--output", "json", "list"},
		{"--output", "json", "info", "scratch"},
		{"--output", "json", "image", "list"},
		{"--output", "json", "image", "inspect", "nixos"},
		{"--output", "json", "forward", "ls"},
		{"--output", "json", "share", "ls", "scratch"},
		{"--output", "json", "config", "show", "scratch"},
	}
	for _, args := range jsonCommands {
		out := runCmd(t, args...)
		var v any
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatalf("voom %s emitted invalid JSON: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	if err := runCmdErr("--output", "json", "rm", "scratch"); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected JSON rm force requirement, got %v", err)
	}
	if err := runCmdErr("logs", "scratch", "--kind", "bad"); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("expected unavailable log error, got %v", err)
	}
	if err := runCmdErr("nixos", "switch", "scratch", "--flake", ".#scratch"); err == nil || !strings.Contains(err.Error(), "nixosSwitch capability is false") {
		t.Fatalf("expected nixos capability gate, got %v", err)
	}
}

func TestRMConfirmationAndForceBehavior(t *testing.T) {
	dir := t.TempDir()
	root := copyFixtureState(t, dir)
	t.Setenv("VOOM_STATE_DIR", root)
	t.Setenv("VOOM_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("VOOM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("VOOM_RUNTIME_DIR", filepath.Join(dir, "runtime"))

	origStdin := os.Stdin
	origTerminal := isTerminalFunc
	t.Cleanup(func() {
		os.Stdin = origStdin
		isTerminalFunc = origTerminal
	})
	isTerminalFunc = func(*os.File) bool { return true }

	cancelInput := filepath.Join(dir, "cancel-input")
	if err := os.WriteFile(cancelInput, []byte("wrong\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(cancelInput)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = f
	if err := runCmdErr("rm", "scratch"); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("expected cancelled remove, got %v", err)
	}
	_ = f.Close()
	if _, err := state.Open(); err != nil {
		t.Fatalf("cancelled remove corrupted store: %v", err)
	}

	confirmInput := filepath.Join(dir, "confirm-input")
	if err := os.WriteFile(confirmInput, []byte("scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err = os.Open(confirmInput)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = f
	out := runCmd(t, "rm", "scratch")
	if !strings.Contains(out, "removed VM scratch") {
		t.Fatalf("unexpected rm output: %s", out)
	}
	_ = f.Close()
	if err := runCmdErr("info", "scratch"); err == nil || !strings.Contains(err.Error(), "no such VM") {
		t.Fatalf("expected confirmed remove to delete VM, got %v", err)
	}
}

func TestDebugPathsJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("VOOM_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("VOOM_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("VOOM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("VOOM_RUNTIME_DIR", filepath.Join(dir, "runtime"))
	out := runCmd(t, "--output", "json", "debug", "paths")
	var p host.Paths
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatal(err)
	}
	if p.State != filepath.Join(dir, "state") || p.Runtime != filepath.Join(dir, "runtime") {
		t.Fatalf("unexpected paths: %#v", p)
	}
}

func TestFixtureCommands(t *testing.T) {
	dir := t.TempDir()
	root := copyFixtureState(t, dir)
	t.Setenv("VOOM_STATE_DIR", root)
	t.Setenv("VOOM_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("VOOM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("VOOM_RUNTIME_DIR", filepath.Join(dir, "runtime"))

	if out := runCmd(t, "list"); !strings.Contains(out, "scratch") || !strings.Contains(out, "4096MiB") {
		t.Fatalf("fixture list missing scratch:\n%s", out)
	}
	var listRows []struct {
		Name      string `json:"name"`
		CPUs      int    `json:"cpus"`
		MemoryMiB int    `json:"memoryMiB"`
	}
	if out := runCmd(t, "--output", "json", "list"); json.Unmarshal([]byte(out), &listRows) != nil || len(listRows) != 1 || listRows[0].Name != "scratch" || listRows[0].CPUs != 4 || listRows[0].MemoryMiB != 4096 {
		t.Fatalf("fixture list JSON missing resources:\n%s", out)
	}
	if out := runCmd(t, "info", "scratch"); !strings.Contains(out, "ssh: root@127.0.0.1:2222") || !strings.Contains(out, "cpus: 4") || !strings.Contains(out, "memory: 4096MiB") {
		t.Fatalf("fixture info mismatch:\n%s", out)
	}
	if out := runCmd(t, "image", "inspect", "nixos"); !strings.Contains(out, "name: nixos") {
		t.Fatalf("fixture image inspect mismatch:\n%s", out)
	}
	if err := runCmdErr("guest", "ports", "scratch"); err == nil || !strings.Contains(err.Error(), "controlShare capability is false") {
		t.Fatalf("expected fixture guest ports capability gate, got %v", err)
	}
}

func importTestImage(t *testing.T, dir, name string) {
	t.Helper()
	format := testImageFormat()
	image := filepath.Join(dir, name+"."+format)
	meta := filepath.Join(dir, name+"."+format+".meta.json")
	if err := os.WriteFile(image, []byte(format), 0o644); err != nil {
		t.Fatal(err)
	}
	metaJSON := `{"user":"root","system":"` + host.System() + `","format":"` + format + `","kernelPath":"","initrdPath":"","initPath":"","kernelCmdline":[],"flake_rev":"abc","baked_at":"2026-05-21T00:00:00Z","capabilities":{"controlShare":true,"guestPortReport":true,"guestShareMount":true,"nixosSwitch":true}}`
	if err := os.WriteFile(meta, []byte(metaJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	runCmd(t, "image", "import", name, image, "--meta", meta)
}

func testImageFormat() string {
	if runtime.GOOS == "darwin" {
		return "raw"
	}
	return "qcow2"
}

func runCmd(t *testing.T, args ...string) string {
	t.Helper()
	var out, errb bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("voom %s failed: %v\nstderr:%s\nstdout:%s", strings.Join(args, " "), err, errb.String(), out.String())
	}
	return out.String()
}

func runCmdErr(args ...string) error {
	var out, errb bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	return cmd.Execute()
}

func withHostPortAvailable(t *testing.T, fn state.HostPortAvailableFunc) {
	t.Helper()
	old := openStore
	openStore = func() (*state.Store, error) {
		return state.Open(state.WithHostPortAvailable(fn))
	}
	t.Cleanup(func() { openStore = old })
}

func copyFixtureState(t *testing.T, dir string) string {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("..", "..", "testdata", "state"))
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "state")
	err = filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestSSHPortReservationsPersistAcrossStoppedVMs(t *testing.T) {
	withHostPortAvailable(t, func(string, int) (bool, string) { return true, "" })
	dir := t.TempDir()
	t.Setenv("VOOM_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("VOOM_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("VOOM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("VOOM_RUNTIME_DIR", filepath.Join(dir, "runtime"))
	importTestImage(t, dir, "nixos")

	runCmd(t, "create", "alpha", "--image", "nixos", "--memory", "512MiB")
	runCmd(t, "create", "beta", "--image", "nixos", "--memory", "512MiB")
	st, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	alpha, err := st.LoadVM("alpha")
	if err != nil {
		t.Fatal(err)
	}
	beta, err := st.LoadVM("beta")
	if err != nil {
		t.Fatal(err)
	}
	if alpha.Network.SSHPort == beta.Network.SSHPort {
		t.Fatalf("stopped VMs share SSH port %d", alpha.Network.SSHPort)
	}
	if err := runCmdErr("forward", "add", "beta", "8080", "--host-port", strconv.Itoa(alpha.Network.SSHPort)); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("expected stopped VM SSH reservation to block forward, got %v", err)
	}
}
