package cli

import (
	"reflect"
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
		},
	}
	want := []string{
		"voom share add dev code /home/u/src /mnt/code",
		"voom share add dev data '/home/u/my data' /mnt/data --ro",
		"voom forward add dev 8080 --host-port 18080",
		"voom forward add dev 5432 --host-port 5432 --bind 0.0.0.0",
		"voom forward auto enable dev --offset 10000",
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
