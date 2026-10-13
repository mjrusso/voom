package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellCompletion(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	t.Setenv("VOOM_STATE_DIR", stateDir)
	for _, kind := range []string{"CONFIG", "CACHE", "RUNTIME"} {
		t.Setenv("VOOM_"+kind+"_DIR", filepath.Join(dir, strings.ToLower(kind)))
	}
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		if out := runCmd(t, "completion", shell); !strings.Contains(out, "voom") {
			t.Fatalf("%s script: %s", shell, out)
		}
	}
	if out := runCmd(t, "__complete", "start", ""); out != ":4\n" {
		t.Fatalf("empty state completion: %q", out)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("completion created state: %v %v", entries, err)
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	index := []byte(`{"schemaVersion":1,"vms":{"scratch":"vm-1","scratch-two":"vm-2","other":"vm-3"},"images":{"nixos":"im-1","debian":"im-2"}}`)
	path := filepath.Join(stateDir, "state.json")
	if err := os.WriteFile(path, index, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"__complete", "shutdown", "sc"}, "scratch\nscratch-two\n:4\n"},
		{[]string{"__complete", "forward", "list", "sc"}, "scratch\nscratch-two\n:4\n"},
		{[]string{"__complete", "image", "info", "ni"}, "nixos\n:4\n"},
		{[]string{"__complete", "create", "new", "--image", "de"}, "debian\n:4\n"},
		{[]string{"__complete", "disk", "reset", "scratch", "--image", "ni"}, "nixos\n:4\n"},
		{[]string{"__complete", "clone", "scratch", "sc"}, ":4\n"},
		{[]string{"__complete", "create", "sc"}, ":4\n"},
		{[]string{"__complete", "image", "import", "new", ""}, ":0\n"},
		{[]string{"__complete", "share", "add", "scratch", "code", ""}, ":0\n"},
	} {
		if out := runCmd(t, tc.args...); out != tc.want {
			t.Fatalf("%v: %q, want %q", tc.args, out, tc.want)
		}
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != string(index) {
		t.Fatalf("completion changed index: %q %v", after, err)
	}
	if err := os.WriteFile(path, []byte(`{"schemaVersion":99}`), 0600); err != nil {
		t.Fatal(err)
	}
	if out := runCmd(t, "__complete", "start", ""); out != ":5\n" {
		t.Fatalf("invalid state completion: %q", out)
	}
}
