package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/share"
	"github.com/mjrusso/voom/internal/state"
)

func TestReplayCommands(t *testing.T) {
	vm := &state.VMRecord{
		Name: "dev",
		Shares: []share.Decl{
			{Tag: "code", HostPath: "/home/u/src", GuestPath: "/mnt/code"},
			{Tag: "data", HostPath: "/home/u/my data", GuestPath: "/mnt/data", Readonly: true},
		},
		Network: state.VMNetwork{
			Forwards: []forward.Decl{
				{Protocol: "tcp", GuestPort: 8080, HostPort: 18080, Bind: "127.0.0.1"},
				{Protocol: "tcp", GuestPort: 5432, HostPort: 5432, Bind: "0.0.0.0"},
			},
			AutoForward:           true,
			AutoForwardHostOffset: 10000,
			AutoForwardBind:       "0.0.0.0",
		},
	}
	want := []string{
		"voom share add dev code /home/u/src /mnt/code",
		"voom share add dev data '/home/u/my data' /mnt/data --ro",
		"voom forward add dev 8080 --host-port 18080",
		"voom forward add dev 5432 --host-port 5432 --bind 0.0.0.0",
		"voom forward auto enable dev --offset 10000 --bind 0.0.0.0",
	}
	if got := replayCommands(vm, "dev"); !reflect.DeepEqual(got, want) {
		t.Fatalf("replayCommands =\n%#v\nwant\n%#v", got, want)
	}
}

func TestReplayCommandsRetargetsName(t *testing.T) {
	vm := &state.VMRecord{
		Name:    "dev",
		Shares:  []share.Decl{{Tag: "code", HostPath: "/src", GuestPath: "/mnt/code"}},
		Network: state.VMNetwork{AutoForward: true},
	}
	want := []string{
		"voom share add clone1 code /src /mnt/code",
		"voom forward auto enable clone1",
	}
	if got := replayCommands(vm, "clone1"); !reflect.DeepEqual(got, want) {
		t.Fatalf("replayCommands retargeted =\n%#v\nwant\n%#v", got, want)
	}
}

func TestReplayCommandsEmptyForUnconfiguredVM(t *testing.T) {
	vm := &state.VMRecord{Name: "bare"}
	if got := replayCommands(vm, "bare"); len(got) != 0 {
		t.Fatalf("expected no commands, got %#v", got)
	}
}

func TestCloneEmitsRetargetedReproductionCommands(t *testing.T) {
	withHostPortAvailable(t, func(string, int) (bool, string) { return true, "" })
	dir := t.TempDir()
	t.Setenv("VOOM_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("VOOM_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("VOOM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("VOOM_RUNTIME_DIR", filepath.Join(dir, "runtime"))
	importTestImage(t, dir, "nixos")

	runCmd(t, "create", "src", "--image", "nixos", "--memory", "512MiB")
	hostShare := filepath.Join(dir, "share")
	if err := os.Mkdir(hostShare, 0o755); err != nil {
		t.Fatal(err)
	}
	runCmd(t, "share", "add", "src", "code", hostShare, "/mnt/code")
	runCmd(t, "forward", "add", "src", "8080", "--host-port", "18080")

	out := runCmd(t, "clone", "src", "dst")
	if !strings.Contains(out, "cloned VM src to dst") {
		t.Fatalf("missing clone result line:\n%s", out)
	}
	if !strings.Contains(out, "voom share add dst code "+hostShare+" /mnt/code") {
		t.Fatalf("missing retargeted share command:\n%s", out)
	}
	if !strings.Contains(out, "voom forward add dst 8080 --host-port 18080") {
		t.Fatalf("missing retargeted forward command:\n%s", out)
	}

	// The clone itself inherits none of that configuration.
	st, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	dst, err := st.LoadVM("dst")
	if err != nil {
		t.Fatal(err)
	}
	if len(dst.Shares) != 0 || len(dst.Network.Forwards) != 0 {
		t.Fatalf("clone inherited config: %#v", dst)
	}

	// JSON surfaces the same commands under configCommands.
	out = runCmd(t, "--output", "json", "clone", "src", "dst2")
	var res struct {
		ConfigCommands []string `json:"configCommands"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.ConfigCommands) != 2 {
		t.Fatalf("expected 2 reproduction commands in JSON, got %#v", res.ConfigCommands)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/home/u/src":  "/home/u/src",
		"":             "''",
		"with space":   "'with space'",
		"a'b":          `'a'\''b'`,
		"semi;colon":   "'semi;colon'",
		"127.0.0.1":    "127.0.0.1",
		"voom-control": "voom-control",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
